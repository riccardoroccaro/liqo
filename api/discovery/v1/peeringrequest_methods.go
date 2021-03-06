package v1

import (
	"context"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

func (pr *PeeringRequest) GetConfig(clientset kubernetes.Interface) (*rest.Config, error) {
	return getConfig(clientset, pr.Spec.KubeConfigRef)
}

type LoadConfigError struct {
	error string
}

func (lce LoadConfigError) Error() string {
	return lce.error
}

func getConfig(clientset kubernetes.Interface, reference *v1.ObjectReference) (*rest.Config, error) {
	secret, err := clientset.CoreV1().Secrets(reference.Namespace).Get(context.TODO(), reference.Name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	kubeconfig := func() (*clientcmdapi.Config, error) {
		return clientcmd.Load(secret.Data["kubeconfig"])
	}
	cnf, err := clientcmd.BuildConfigFromKubeconfigGetter("", kubeconfig)
	if err != nil {
		return nil, LoadConfigError{
			error: err.Error(),
		}
	}
	return cnf, nil
}
