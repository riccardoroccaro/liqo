/*

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package advertisement_operator

import (
	"context"
	"fmt"
	"github.com/apparentlymart/go-cidr/cidr"
	"github.com/go-logr/logr"
	dronetOperator "github.com/netgroup-polito/dronev2/pkg/dronet-operator"
	"github.com/pkg/errors"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"net"
	"os"

	"k8s.io/apimachinery/pkg/runtime"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	protocolv1 "github.com/netgroup-polito/dronev2/api/advertisement-operator/v1"
	dronetv1 "github.com/netgroup-polito/dronev2/api/tunnel-endpoint/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

const (
	tunEndpointNameSuffix = "-tunendpoint"
	defualtPodCIDRValue   = "None"
)

// AdvertisementReconciler reconciles a Advertisement object
type TunnelEndpointCreator struct {
	client.Client
	Log               logr.Logger
	Scheme            *runtime.Scheme
	UsedSubnets       map[string]*net.IPNet
	FreeSubnets       map[string]*net.IPNet
	TunnelEndpointMap map[string]types.NamespacedName
}

// +kubebuilder:rbac:groups=protocol.drone.com,resources=advertisements,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=protocol.drone.com,resources=advertisements/status,verbs=get;update;patch

//rbac for the dronet.drone.com api
// +kubebuilder:rbac:groups=dronet.drone.com,resources=tunnelendpoints,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=dronet.drone.com,resources=tunnelendpoints/status,verbs=get;update;patch

func (r *TunnelEndpointCreator) Reconcile(req ctrl.Request) (ctrl.Result, error) {
	ctx := context.Background()
	log := r.Log.WithValues("tunnelEndpointCreator-controller", req.NamespacedName)
	tunnelEndpointCreatorFinalizer := "tunnelEndpointCreator-Finalizer.dronet.drone.com"
	// get advertisement
	var adv protocolv1.Advertisement
	if err := r.Get(ctx, req.NamespacedName, &adv); err != nil {
		// reconcile was triggered by a delete request
		log.Info("Advertisement " + req.Name + " deleted")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	// examine DeletionTimestamp to determine if object is under deletion
	if adv.ObjectMeta.DeletionTimestamp.IsZero() {
		if !dronetOperator.ContainsString(adv.ObjectMeta.Finalizers, tunnelEndpointCreatorFinalizer) {
			// The object is not being deleted, so if it does not have our finalizer,
			// then lets add the finalizer and update the object. This is equivalent
			// registering our finalizer.
			adv.ObjectMeta.Finalizers = append(adv.Finalizers, tunnelEndpointCreatorFinalizer)
			if err := r.Update(ctx, &adv); err != nil {
				//while updating we check if the a resource version conflict happened
				//which means the version of the object we have is outdated.
				//a solution could be to return an error and requeue the object for later process
				//but if the object has been changed by another instance of the controller running in
				//another host it already has been put in the working queue so decide to forget the
				//current version and process the next item in the queue assured that we handle the object later
				if apierrors.IsConflict(err) {
					return ctrl.Result{}, nil
				}
				log.Error(err, "unable to update adv", "adv", adv.Name)
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}
	} else {
		//the object is being deleted
		if dronetOperator.ContainsString(adv.Finalizers, tunnelEndpointCreatorFinalizer) {
			if err := r.deleteTunEndpoint(&adv); err != nil {
				log.Error(err, "error while deleting endpoint")
				return ctrl.Result{}, err
			}
			//remove the finalizer from the list and update it.
			adv.Finalizers = dronetOperator.RemoveString(adv.Finalizers, tunnelEndpointCreatorFinalizer)
			if err := r.Update(ctx, &adv); err != nil {
				if apierrors.IsConflict(err) {
					return ctrl.Result{}, nil
				}
				log.Error(err, "unable to update adv %s", adv.Name, "in namespace", adv.Namespace)
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}

	err := r.createTunEndpoint(&adv)
	if err != nil {
		log.Error(err, "error while creating endpoint")
		return ctrl.Result{}, err
	}
	err = r.updateTunEndpoint(&adv)
	if err != nil {
		log.Error(err, "unable to update tunelEndpointCR")
	}
	return ctrl.Result{}, nil
}

func (r *TunnelEndpointCreator) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&protocolv1.Advertisement{}).
		Complete(r)
}

func (r *TunnelEndpointCreator) getNamespace() (namespace string, err error) {
	//it is passed to the pod during the deployment; in its manifest
	keyNamespace := "POD_NAMESPACE"
	namespace, found := os.LookupEnv(keyNamespace)
	if !found {
		return namespace, errors.New("the environment variable " + keyNamespace + "is not set. ")
	}
	return namespace, nil
}

func (r *TunnelEndpointCreator) getTunnelEndpointByKey(key types.NamespacedName) (tunEndpoint *dronetv1.TunnelEndpoint, err error) {
	ctx := context.Background()
	tunEndpoint = new(dronetv1.TunnelEndpoint)
	err = r.Get(ctx, key, tunEndpoint)
	if err != nil {
		return tunEndpoint, err
	}
	return tunEndpoint, err
}

func (r *TunnelEndpointCreator) getTunnelEndpointByName(name string) (tunEndpoint *dronetv1.TunnelEndpoint, err error) {
	ctx := context.Background()
	//create the key to be used to retrieve the CR
	namespace, err := r.getNamespace()
	if err != nil {
		return tunEndpoint, err
	}
	key := types.NamespacedName{
		Namespace: namespace,
		Name:      name,
	}
	//try to retrieve the CR
	err = r.Get(ctx, key, tunEndpoint)
	if err != nil {
		return tunEndpoint, err
	}
	return tunEndpoint, err
}

func (r *TunnelEndpointCreator) getTunEndPerADV(adv *protocolv1.Advertisement) (tunEndpoint *dronetv1.TunnelEndpoint, err error) {
	//check if a tunnelEndpointCR has been created for the current ADV
	tunEndpointKey := adv.Status.TunnelEndpointKey
	if tunEndpointKey.Name != "" && tunEndpointKey.Namespace != "" {
		tunEndpoint, err := r.getTunnelEndpointByKey(types.NamespacedName(tunEndpointKey))
		if err == nil {
			return tunEndpoint, err
		} else if apierrors.IsNotFound(err) {
			return tunEndpoint, errors.New("notFound")
		} else {
			return tunEndpoint, err
		}
	} else {
		return tunEndpoint, errors.New("notFound")
	}
}

func (r *TunnelEndpointCreator) isTunEndpointUpdated(adv *protocolv1.Advertisement, tunEndpoint *dronetv1.TunnelEndpoint) bool {
	if adv.Spec.ClusterId == tunEndpoint.Spec.ClusterID && adv.Spec.Network.PodCIDR == tunEndpoint.Spec.PodCIDR && adv.Spec.Network.GatewayIP == tunEndpoint.Spec.TunnelPublicIP && adv.Spec.Network.GatewayPrivateIP == tunEndpoint.Spec.TunnelPrivateIP {
		return true
	} else {
		return false
	}
}

func (r *TunnelEndpointCreator) createOrUpdateTunEndpoint(adv *protocolv1.Advertisement, advKey types.NamespacedName) error {
	tunEndpoint, err := r.getTunEndPerADV(adv)
	if err == nil {
		equal := r.isTunEndpointUpdated(adv, tunEndpoint)
		if equal {
			return nil
		} else {
			err := r.updateTunEndpoint(adv)
			if err == nil {
				return nil
			} else {
				return err
			}
		}
	} else if err.Error() == "notFound" {
		err := r.createTunEndpoint(adv)
		if err == nil {
			return nil
		} else {
			return err
		}
	} else {
		return err
	}
}

func (r *TunnelEndpointCreator) updateTunEndpoint(adv *protocolv1.Advertisement) error {
	//funcName := "updateTunEndpoint"
	ctx := context.Background()
	//log := r.Log.WithValues("tunnelEndpointCreator-controller", funcName)
	var tunEndpoint dronetv1.TunnelEndpoint
	var remoteRemappedPodCIDR string
	//build the key used to retrieve the tunnelEndpoint CR
	tunEndKey := types.NamespacedName{
		Namespace: adv.Namespace,
		Name:      adv.Spec.ClusterId + tunEndpointNameSuffix,
	}
	//retrieve the tunnelEndpoint CR
	err := r.Get(ctx, tunEndKey, &tunEndpoint)
	//if the tunEndpoint CR can not be retrieved then return the error
	//if this come here it means that the CR has been created because the function is called only if the create process goes well
	if err != nil {
		return err
	}

	if tunEndpoint.Status.Phase == "" {
		//TODO: implement the IPAM algorithm
		//check if the PodCidr of the remote cluster overlaps with any of the subnets on the local cluster
		_, subnet, err := net.ParseCIDR(adv.Spec.Network.PodCIDR)
		if err != nil {
			return fmt.Errorf("an error occured while parsing podCidr %s from adv %s :%v", adv.Spec.Network.PodCIDR, adv.Name, err)
		}
		subnet, isNewSub, err := r.checkSubnet(subnet)
		if err != nil {
			return err
		}
		if isNewSub {
			remoteRemappedPodCIDR = subnet.String()
			//update adv status
			adv.Status.RemoteRemappedPodCIDR = remoteRemappedPodCIDR
			err := r.Status().Update(ctx, adv)
			if err != nil {
				return err
			}
			//update tunEndpoint status
			tunEndpoint.Status.RemoteRemappedPodCIDR = remoteRemappedPodCIDR
			tunEndpoint.Status.Phase = "New"
			err = r.Status().Update(ctx, &tunEndpoint)
			if err != nil {
				return err
			}
		} else {
			//update adv status
			adv.Status.RemoteRemappedPodCIDR = defualtPodCIDRValue
			err := r.Status().Update(ctx, adv)
			if err != nil {
				return err
			}
			//update tunEndpoint status
			tunEndpoint.Status.RemoteRemappedPodCIDR = defualtPodCIDRValue
			tunEndpoint.Status.Phase = "New"
			err = r.Status().Update(ctx, &tunEndpoint)
			if err != nil {
				return err
			}
		}
		//update IPAM
		r.updateIPAM(subnet)
		return nil
	} else if tunEndpoint.Status.Phase == "New" {
		if adv.Status.LocalRemappedPodCIDR == "" {
			return nil
		}else {
			tunEndpoint.Status.LocalRemappedPodCIDR = adv.Status.LocalRemappedPodCIDR
			tunEndpoint.Status.Phase = "Processed"
			err = r.Status().Update(ctx, &tunEndpoint)
			if err != nil {
				return err
			}
		}
		return nil
	} else {
		return nil
	}
}

//we do not support updates to the ADV CR by the user, at least not yet
//we assume that the ADV is created by the Remote Server and is the only one who can remove or update it
//if the ADV has to be updated first we remove it and then recreate the it with new values
//For each ADV CR a tunnelEndpoint CR is created in the same namespace as the ADV CR and the name of the later
//is derived from the name of the ADV adding a suffix. Doing so, given an ADV we can always create, update or delete
//the associated tunnelEndpoint CR without the necessity for its key to be saved by the operator.
func (r *TunnelEndpointCreator) createTunEndpoint(adv *protocolv1.Advertisement) error {
	funcName := "createTunEndpoint"
	ctx := context.Background()
	log := r.Log.WithValues("tunnelEndpointCreator-controller", funcName)
	var tunEndpoint dronetv1.TunnelEndpoint

	//build the key used to retrieve the tunnelEndpoint CR
	tunEndKey := types.NamespacedName{
		Namespace: adv.Namespace,
		Name:      adv.Spec.ClusterId + tunEndpointNameSuffix,
	}
	//retrieve the tunnelEndpoint CR
	err := r.Get(ctx, tunEndKey, &tunEndpoint)
	//if the CR exist then do nothing and return
	if err == nil {
		return nil
	} else if apierrors.IsNotFound(err) {
		//if tunnelEndpoint referenced by the key does not exist then we create it
		tunEndpoint := &dronetv1.TunnelEndpoint{
			ObjectMeta: v1.ObjectMeta{
				//the name is derived from the clusterID
				Name: adv.Spec.ClusterId + tunEndpointNameSuffix,
				//the namespace is read from the Environment variable passe to the pod
				Namespace: adv.Namespace,
			},
			Spec: dronetv1.TunnelEndpointSpec{
				ClusterID:       adv.Spec.ClusterId,
				PodCIDR:         adv.Spec.Network.PodCIDR,
				TunnelPublicIP:  adv.Spec.Network.GatewayIP,
				TunnelPrivateIP: adv.Spec.Network.GatewayPrivateIP,
			},
			Status: dronetv1.TunnelEndpointStatus{},
		}
		err = r.Create(ctx, tunEndpoint)
		if err != nil {
			log.Info("failed to create the custom resource", "name", tunEndpoint.Name, "namespace", tunEndpoint.Namespace, "clusterId", adv.Spec.ClusterId, "podCIDR", adv.Spec.Network.PodCIDR, "gatewayPublicIP", adv.Spec.Network.GatewayIP, "tunnelPrivateIP", adv.Spec.Network.GatewayPrivateIP)
			return err
		}
		log.Info("created the custom resource", "name", tunEndpoint.Name, "namespace", tunEndpoint.Namespace, "clusterId", adv.Spec.ClusterId, "podCIDR", adv.Spec.Network.PodCIDR, "gatewayPublicIP", adv.Spec.Network.GatewayIP, "tunnelPrivateIP", adv.Spec.Network.GatewayPrivateIP)
		return nil
	} else {
		return err
	}
}

func (r *TunnelEndpointCreator) InitIPAM() error {
	//TODO: remove the hardcoded value of the CIDRBlock
	CIDRBlock := "10.0.0.0/16"
	//the first /16 subnet in 10/8 cidr block
	_, subnet, err := net.ParseCIDR("10.0.0.0/16")
	if err != nil {
		r.Log.Error(err, "unable to parse the first subnet %s :%v", CIDRBlock, err)
		return err
	}
	//first we get podCIDR and clusterCIDR
	podCIDR, err := dronetOperator.GetClusterPodCIDR()
	if err != nil {
		r.Log.Error(err, "unable to retrieve podCIDR from environment variable")
		return err
	}
	clusterCIDR, err := dronetOperator.GetClusterCIDR()
	if err != nil {
		r.Log.Error(err, "unable to retrieve clusterCIDR from environment variable")
		return err
	}
	//we parse podCIDR and clusterCIDR
	_, clusterNet, err := net.ParseCIDR(clusterCIDR)
	if err != nil {
		return fmt.Errorf("an error occured while parsing clusterCIDR %s :%v", clusterCIDR, err)
	}
	_, podNet, err := net.ParseCIDR(podCIDR)
	if err != nil {
		return fmt.Errorf("an error occured while parsing podCIDR %s :%v", podCIDR, err)
	}
	//The first subnet /16 is added to the freeSubnets
	r.FreeSubnets[subnet.String()] = subnet
	//here we divide the CIDRBlock 10.0.0.0/8 in 256 /16 subnets
	for i := 0; i < 255; i++ {
		subnet, _ = cidr.NextSubnet(subnet, 16)
		r.FreeSubnets[subnet.String()] = subnet
	}
	//clusterCIDR and podCIDR are added to the usedSubnets
	r.UsedSubnets[clusterNet.String()] = clusterNet
	r.UsedSubnets[podNet.String()] = podNet

	//we move all the subnets that have conflicts with the podCidr and clusterCidr from freeSubnets to usedSubnets
	for _, net := range r.FreeSubnets {
		if bool := dronetOperator.VerifyNoOverlap(r.UsedSubnets, net); bool {
			if _, ok := r.UsedSubnets[net.String()]; !ok {
				r.UsedSubnets[net.String()] = net
				delete(r.FreeSubnets, net.String())
			} else {
				delete(r.FreeSubnets, net.String())
			}
		}
	}
	return nil
}

func (r *TunnelEndpointCreator) getNextSubnetAvail() (*net.IPNet, error) {
	if len(r.FreeSubnets) == 0 {
		return nil, fmt.Errorf("no more available subnets to allocate")
	}
	var availableSubnet *net.IPNet
	for _, subnet := range r.FreeSubnets {
		availableSubnet = subnet
		break
	}
	return availableSubnet, nil
}

func (r *TunnelEndpointCreator) checkSubnet(network *net.IPNet) (*net.IPNet, bool, error) {
	//check if the given network has conflicts with any of the used subnets
	if flag := dronetOperator.VerifyNoOverlap(r.UsedSubnets, network); flag {
		//if there are conflicts then get a free subnet from the pool and return it
		//return also a "true" value for the bool
		if subnet, err := r.getNextSubnetAvail(); err != nil {
			return nil, false, err
		} else {
			return subnet, true, nil
		}
	}
	return network, false, nil
}

//add the network to the usedSubnets and remove of the subnets in free subnets that overlap with the network
func (r *TunnelEndpointCreator) updateIPAM(network *net.IPNet) {
	r.UsedSubnets[network.String()] = network
	for _, net := range r.FreeSubnets {
		if bool := dronetOperator.VerifyNoOverlap(r.UsedSubnets, net); bool {
			if _, ok := r.UsedSubnets[net.String()]; !ok {
				r.UsedSubnets[net.String()] = net
				delete(r.FreeSubnets, net.String())
			} else {
				delete(r.FreeSubnets, net.String())
			}
		}
	}
}

func (r *TunnelEndpointCreator) deleteTunEndpoint(adv *protocolv1.Advertisement) error {
	ctx := context.Background()
	var tunEndpoint dronetv1.TunnelEndpoint
	//build the key used to retrieve the tunnelEndpoint CR
	tunEndKey := types.NamespacedName{
		Namespace: adv.Namespace,
		Name:      adv.Spec.ClusterId + tunEndpointNameSuffix,
	}
	//retrieve the tunnelEndpoint CR
	err := r.Get(ctx, tunEndKey, &tunEndpoint)
	//if the CR exist then do nothing and return
	if err == nil {
		err := r.Delete(ctx, &tunEndpoint)
		if err != nil {
			return fmt.Errorf("unable to delete endpoint %s in namespace %s : %v", tunEndpoint.Name, tunEndpoint.Namespace, err)
		} else {
			return nil
		}
	} else if apierrors.IsNotFound(err) {
		return nil
	} else {
		return fmt.Errorf("unable to get endpoint with key %s: %v", tunEndKey.String(), err)
	}
}
