package advertisement_operator

import (
	protocolv1 "github.com/liqoTech/liqo/api/advertisement-operator/v1"
	configv1alpha1 "github.com/liqoTech/liqo/api/config/v1alpha1"
	"github.com/liqoTech/liqo/pkg/clusterConfig"
	"github.com/liqoTech/liqo/pkg/crdClient"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog"
	"time"
)

func (b *AdvertisementBroadcaster) WatchConfiguration(kubeconfigPath string, client *crdClient.CRDClient) {
	go clusterConfig.WatchConfiguration(func(configuration *configv1alpha1.ClusterConfig) {
		if !configuration.Spec.AdvertisementConfig.EnableBroadcaster {
			klog.V(3).Info("ClusterConfig changed")
			klog.Info("Stopping sharing resources with cluster " + b.ForeignClusterId)
			err := b.NotifyAdvertisementDeletion()
			if err != nil {
				klog.Errorln(err, "Unable to notify Advertisement deletion to foreign cluster")
			} else {
				// wait for advertisement to be deleted to delete the peering request
				for retry := 0; retry < 3; retry++ {
					advName := "advertisement-" + b.HomeClusterId
					if _, err := b.RemoteClient.Resource("advertisements").Get(advName, metav1.GetOptions{}); err != nil && k8serrors.IsNotFound(err) {
						break
					}
					time.Sleep(30 * time.Second)
				}
			}
			// delete the peering request to delete the broadcaster
			if err := b.DiscoveryClient.Resource("peeringrequests").Delete(b.PeeringRequestName, metav1.DeleteOptions{}); err != nil {
				klog.Error("Unable to delete PeeringRequest " + b.PeeringRequestName)
			}
		}

		if configuration.Spec.AdvertisementConfig.ResourceSharingPercentage != b.ClusterConfig.AdvertisementConfig.ResourceSharingPercentage {
			klog.V(3).Info("ClusterConfig changed")
			b.ClusterConfig.AdvertisementConfig.ResourceSharingPercentage = configuration.Spec.AdvertisementConfig.ResourceSharingPercentage
			// update Advertisement with new resources (given by the new sharing percentage)
			physicalNodes, virtualNodes, availability, limits, images, err := b.GetResourcesForAdv()
			if err != nil {
				klog.Errorln(err, "Error while computing resources for Advertisement")
			}
			advToCreate := b.CreateAdvertisement(physicalNodes, virtualNodes, availability, images, limits)
			_, err = b.SendAdvertisementToForeignCluster(advToCreate)
			if err != nil {
				klog.Errorln(err, "Error while sending Advertisement to cluster "+b.ForeignClusterId)
			}
		}

	}, client, kubeconfigPath)
}

func (r *AdvertisementReconciler) WatchConfiguration(kubeconfigPath string) {
	go clusterConfig.WatchConfiguration(func(configuration *configv1alpha1.ClusterConfig) {
		if configuration.Spec.AdvertisementConfig.AutoAccept != r.ClusterConfig.AutoAccept ||
			configuration.Spec.AdvertisementConfig.MaxAcceptableAdvertisement != r.ClusterConfig.MaxAcceptableAdvertisement {
			klog.V(3).Info("ClusterConfig changed")
			obj, err := r.AdvClient.Resource("advertisements").List(metav1.ListOptions{})
			if err != nil {
				klog.Error(err, "Unable to apply configuration: error listing Advertisements")
				return
			}
			advList := obj.(*protocolv1.AdvertisementList)
			err, updateFlag := r.ManageConfigUpdate(configuration, advList)
			if err != nil {
				klog.Error(err, err.Error())
				return
			}
			if updateFlag {
				for i := range advList.Items {
					adv := advList.Items[i]
					r.UpdateAdvertisement(&adv)
				}
			}
		}
	}, nil, kubeconfigPath)
}

func (r *AdvertisementReconciler) ManageConfigUpdate(configuration *configv1alpha1.ClusterConfig, advList *protocolv1.AdvertisementList) (error, bool) {

	updateFlag := false
	if configuration.Spec.AdvertisementConfig.MaxAcceptableAdvertisement > r.ClusterConfig.MaxAcceptableAdvertisement {
		// the maximum has increased: check if there are refused advertisements which now can be accepted
		r.ClusterConfig = configuration.Spec.AdvertisementConfig
		for i := 0; i < len(advList.Items); i++ {
			adv := &advList.Items[i]
			if adv.Status.AdvertisementStatus == AdvertisementRefused {
				r.CheckAdvertisement(adv)
				updateFlag = true
			}
		}
	} else {
		// the maximum has decreased: if the already accepted advertisements are too many (with the new maximum), delete some of them
		r.ClusterConfig = configuration.Spec.AdvertisementConfig
		if r.ClusterConfig.MaxAcceptableAdvertisement < r.AcceptedAdvNum {
			for i := 0; i < int(r.AcceptedAdvNum-r.ClusterConfig.MaxAcceptableAdvertisement); i++ {
				adv := advList.Items[i]
				if adv.Status.AdvertisementStatus == AdvertisementAccepted {
					err := r.AdvClient.Resource("advertisements").Delete(adv.Name, metav1.DeleteOptions{})
					if err != nil {
						klog.Errorln(err, "Unable to apply configuration: error deleting Advertisement "+adv.Name)
						return err, updateFlag
					}
					r.AcceptedAdvNum--
				}
			}
		}
	}
	return nil, updateFlag
}
