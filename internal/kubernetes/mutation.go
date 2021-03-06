package kubernetes

import (
	"fmt"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"strings"
	"time"
)

func F2HTranslate(podForeignIn *v1.Pod, newCidr, namespace string) (podHomeOut *v1.Pod) {
	podHomeOut = podForeignIn.DeepCopy()
	podHomeOut.SetNamespace(namespace)
	podHomeOut.SetUID(types.UID(podForeignIn.Annotations["home_uuid"]))
	podHomeOut.SetResourceVersion(podForeignIn.Annotations["home_resourceVersion"])
	t, err := time.Parse("2006-01-02 15:04:05 -0700 MST", podForeignIn.Annotations["home_creationTimestamp"])
	if err != nil {
		_ = fmt.Errorf("unable to parse time")
	}
	if podForeignIn.DeletionGracePeriodSeconds != nil {
		metav1.SetMetaDataAnnotation(&podHomeOut.ObjectMeta, "foreign_deletionPeriodSeconds", string(*podForeignIn.DeletionGracePeriodSeconds))
		podHomeOut.DeletionGracePeriodSeconds = nil
	}
	if podHomeOut.Status.PodIP != "" {
		newIp := ChangePodIp(newCidr, podHomeOut.Status.PodIP)
		podHomeOut.Status.PodIP = newIp
		podHomeOut.Status.PodIPs[0].IP = newIp
	}
	podHomeOut.SetCreationTimestamp(metav1.NewTime(t))
	podHomeOut.Spec.NodeName = podForeignIn.Annotations["home_nodename"]
	delete(podHomeOut.Annotations, "home_creationTimestamp")
	delete(podHomeOut.Annotations, "home_resourceVersion")
	delete(podHomeOut.Annotations, "home_uuid")
	delete(podHomeOut.Annotations, "home_nodename")
	return podHomeOut
}

func H2FTranslate(pod *v1.Pod, nattedNS string) *v1.Pod {
	// create an empty ObjectMeta for the output pod, copying only "Name" and "Namespace" fields
	objectMeta := metav1.ObjectMeta{
		Name:      pod.ObjectMeta.Name,
		Namespace: nattedNS,
		Labels:    pod.Labels,
	}

	// filter volumes which can be mounted on the foreign cluster
	volumes := FilterVolumes(pod.Spec.Volumes)
	// copy all containers from input pod
	containers := make([]v1.Container, len(pod.Spec.Containers))
	for i := 0; i < len(pod.Spec.Containers); i++ {
		// filter volumeMounts related to volumes which have been filtered
		volumeMounts := FilterVolumeMounts(volumes, pod.Spec.Containers[i].VolumeMounts)

		containers[i] = v1.Container{
			Name:            pod.Spec.Containers[i].Name,
			Image:           pod.Spec.Containers[i].Image,
			Command:         pod.Spec.Containers[i].Command,
			Args:            pod.Spec.Containers[i].Args,
			WorkingDir:      pod.Spec.Containers[i].WorkingDir,
			Ports:           pod.Spec.Containers[i].Ports,
			Env:             pod.Spec.Containers[i].Env,
			Resources:       pod.Spec.Containers[i].Resources,
			LivenessProbe:   pod.Spec.Containers[i].LivenessProbe,
			ReadinessProbe:  pod.Spec.Containers[i].ReadinessProbe,
			StartupProbe:    pod.Spec.Containers[i].StartupProbe,
			SecurityContext: pod.Spec.Containers[i].SecurityContext,
			VolumeMounts:    volumeMounts,
		}
	}

	affinity := v1.Affinity{
		NodeAffinity: &v1.NodeAffinity{
			RequiredDuringSchedulingIgnoredDuringExecution: &v1.NodeSelector{
				NodeSelectorTerms: []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{
							{
								Key:      "type",
								Operator: v1.NodeSelectorOpNotIn,
								Values:   []string{"virtual-node"},
							},
						},
					},
				},
			},
		},
	}

	// create an empty Spec for the output pod, copying only "Containers" field
	podSpec := v1.PodSpec{
		Containers: containers,
		Affinity:   affinity.DeepCopy(),
		Volumes:    volumes,
		//TODO: check if we need other fields
	}

	metav1.SetMetaDataAnnotation(&objectMeta, "home_nodename", pod.Spec.NodeName)
	metav1.SetMetaDataAnnotation(&objectMeta, "home_resourceVersion", pod.ResourceVersion)
	metav1.SetMetaDataAnnotation(&objectMeta, "home_uuid", string(pod.UID))
	metav1.SetMetaDataAnnotation(&objectMeta, "home_creationTimestamp", pod.CreationTimestamp.String())

	return &v1.Pod{
		TypeMeta:   pod.TypeMeta,
		ObjectMeta: objectMeta,
		Spec:       podSpec,
		Status:     pod.Status,
	}
}

func FilterVolumes(volumesIn []v1.Volume) []v1.Volume {
	volumesOut := make([]v1.Volume, 0)
	for _, v := range volumesIn {
		if v.ConfigMap != nil || v.EmptyDir != nil || v.DownwardAPI != nil {
			volumesOut = append(volumesOut, v)
		}
		// copy all volumes of type Secret except for the default token
		if v.Secret != nil && !strings.Contains(v.Secret.SecretName, "default-token") {
			volumesOut = append(volumesOut, v)
		}
	}
	return volumesOut
}

// remove from volumeMountsIn all the volumeMounts with name not contained in volumes
func FilterVolumeMounts(volumes []v1.Volume, volumeMountsIn []v1.VolumeMount) []v1.VolumeMount {
	volumeMounts := make([]v1.VolumeMount, 0)
	for _, vm := range volumeMountsIn {
		for _, v := range volumes {
			if vm.Name == v.Name {
				volumeMounts = append(volumeMounts, vm)
			}
		}
	}
	return volumeMounts
}

func ChangePodIp(newPodCidr string, oldPodIp string) (newPodIp string) {
	//the last two slices are the suffix of the newPodIp
	oldPodIpTokenized := strings.Split(oldPodIp, ".")
	newPodCidrTokenized := strings.Split(newPodCidr, "/")
	//the first two slices are the prefix of the newPodIP
	ipFromPodCidrTokenized := strings.Split(newPodCidrTokenized[0], ".")
	//used to build the new IP
	var newPodIpBuilder strings.Builder
	for i, s := range ipFromPodCidrTokenized {
		if i < 2 {
			newPodIpBuilder.WriteString(s)
			newPodIpBuilder.WriteString(".")
		}
	}
	for i, s := range oldPodIpTokenized {
		if i > 1 && i < 4 {
			newPodIpBuilder.WriteString(s)
			newPodIpBuilder.WriteString(".")
		}
	}
	return strings.TrimSuffix(newPodIpBuilder.String(), ".")
}
