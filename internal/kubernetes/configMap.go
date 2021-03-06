package kubernetes

import (
	"context"
	"errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/klog"
)

func (p *KubernetesProvider) manageCmEvent(event watch.Event) error {
	var err error

	cm, ok := event.Object.(*corev1.ConfigMap)
	if !ok {
		return errors.New("cannot cast object to configMap")
	}
	klog.V(3).Infof("received %v on configmap %v", event.Type, cm.Name)

	nattedNS, err := p.NatNamespace(cm.Namespace, false)
	if err != nil {
		return err
	}

	switch event.Type {
	case watch.Added:
		_, err := p.foreignClient.Client().CoreV1().ConfigMaps(nattedNS).Get(context.TODO(), cm.Name, metav1.GetOptions{})
		if err != nil {
			klog.Info("remote cm " + cm.Name + " doesn't exist: creating it")

			if err = p.createConfigMap(cm, nattedNS); err != nil {
				klog.Error(err, "unable to create configMap "+cm.Name+" on cluster "+p.foreignClusterId)
			} else {
				klog.V(3).Infof("correctly created configMap %v on cluster %v", cm.Name, p.foreignClusterId)
			}
		}

	case watch.Modified:
		if err = p.updateConfigMap(cm, nattedNS); err != nil {
			klog.Error(err, "unable to update configMap "+cm.Name+" on cluster "+p.foreignClusterId)
		} else {
			klog.V(3).Infof("correctly updated configMap %v on cluster %v", cm.Name, p.foreignClusterId)
		}

	case watch.Deleted:
		if err = p.deleteConfigMap(cm, nattedNS); err != nil {
			klog.Error(err, "unable to delete configMap "+cm.Name+" on cluster "+p.foreignClusterId)
		} else {
			klog.V(3).Infof("correctly deleted configMap %v on cluster %v", cm.Name, p.foreignClusterId)
		}
	}
	return nil
}

func (p *KubernetesProvider) createConfigMap(cm *corev1.ConfigMap, namespace string) error {
	cmRemote := corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:        cm.Name,
			Namespace:   namespace,
			Labels:      cm.Labels,
			Annotations: nil,
		},
		Data:       cm.Data,
		BinaryData: cm.BinaryData,
	}

	if cmRemote.Labels == nil {
		cmRemote.Labels = make(map[string]string)
	}
	cmRemote.Labels["liqo/reflection"] = "reflected"

	_, err := p.foreignClient.Client().CoreV1().ConfigMaps(namespace).Create(context.TODO(), &cmRemote, metav1.CreateOptions{})

	return err
}

func (p *KubernetesProvider) updateConfigMap(cm *corev1.ConfigMap, namespace string) error {
	cmOld, err := p.foreignClient.Client().CoreV1().ConfigMaps(namespace).Get(context.TODO(), cm.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}

	cm2 := cm.DeepCopy()
	cm2.SetNamespace(namespace)
	cm2.SetResourceVersion(cmOld.ResourceVersion)
	cm2.SetUID(cmOld.UID)
	_, err = p.foreignClient.Client().CoreV1().ConfigMaps(namespace).Update(context.TODO(), cm2, metav1.UpdateOptions{})

	return err
}

func (p *KubernetesProvider) deleteConfigMap(cm *corev1.ConfigMap, namespace string) error {
	cm.Namespace = namespace
	err := p.foreignClient.Client().CoreV1().ConfigMaps(namespace).Delete(context.TODO(), cm.Name, metav1.DeleteOptions{})

	return err
}
