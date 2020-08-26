package advertisement_operator

import (
	v1 "github.com/liqoTech/liqo/api/advertisement-operator/v1"
	configv1alpha1 "github.com/liqoTech/liqo/api/config/v1alpha1"
	advcontroller "github.com/liqoTech/liqo/internal/advertisement-operator"
	"github.com/stretchr/testify/assert"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"
)

func createReconciler(acceptedAdv, maxAcceptableAdv int32, autoAccept bool) advcontroller.AdvertisementReconciler {
	return advcontroller.AdvertisementReconciler{
		Client:           nil,
		Scheme:           nil,
		EventsRecorder:   nil,
		KubeletNamespace: "",
		KindEnvironment:  false,
		VKImage:          "",
		InitVKImage:      "",
		HomeClusterId:    "",
		AcceptedAdvNum:   acceptedAdv,
		ClusterConfig: configv1alpha1.AdvertisementConfig{
			MaxAcceptableAdvertisement: maxAcceptableAdv,
			AutoAccept:                 autoAccept,
		},
	}
}

func createAdvertisement() v1.Advertisement {
	return v1.Advertisement{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{},
		Spec:       v1.AdvertisementSpec{},
		Status:     v1.AdvertisementStatus{},
	}
}

func TestAcceptAdvertisementWithAutoAccept(t *testing.T) {
	r := createReconciler(0, 10, true)

	// given a configuration with max 10 Advertisements, create 10 Advertisements and check that they are all accepted
	for i := 0; i < 10; i++ {
		adv := createAdvertisement()
		r.CheckAdvertisement(&adv)
		assert.Equal(t, advcontroller.AdvertisementAccepted, adv.Status.AdvertisementStatus)
	}
	// check that the Adv counter has been incremented
	assert.Equal(t, int32(10), r.AcceptedAdvNum)
}

func TestRefuseAdvertisementWithAutoAccept(t *testing.T) {
	r := createReconciler(0, 10, true)

	// given a configuration with max 10 Advertisements, create 10 Advertisements
	for i := 0; i < 10; i++ {
		adv := createAdvertisement()
		r.CheckAdvertisement(&adv)
	}

	// create 10 more Advertisements and check that they are all refused, since the maximum has been reached
	for i := 0; i < 10; i++ {
		adv := createAdvertisement()
		r.CheckAdvertisement(&adv)
		assert.Equal(t, advcontroller.AdvertisementRefused, adv.Status.AdvertisementStatus)
	}
	// check that the Adv counter has not been modified
	assert.Equal(t, int32(10), r.AcceptedAdvNum)
}

func TestCheckAdvertisementWithoutAutoAccept(t *testing.T) {
	r := createReconciler(0, 10, false)

	// given a configuration with max 10 Advertisements but no AutoAccept, create 10 Advertisements and check they are refused
	for i := 0; i < 10; i++ {
		adv := createAdvertisement()
		r.CheckAdvertisement(&adv)
		assert.Equal(t, advcontroller.AdvertisementRefused, adv.Status.AdvertisementStatus)
	}
	// check that the Adv counter has not been incremented
	assert.Equal(t, int32(0), r.AcceptedAdvNum)
}

func TestManageConfigUpdate(t *testing.T) {
	r := createReconciler(0, 10, true)
	advList := v1.AdvertisementList{
		Items: []v1.Advertisement{},
	}

	advCount := 15

	// given a configuration with max 10 Advertisements, create 15 Advertisement: 10 should be accepted and 5 refused
	for i := 0; i < advCount; i++ {
		adv := createAdvertisement()
		r.CheckAdvertisement(&adv)
		advList.Items = append(advList.Items, adv)
	}

	// the advList contains 10 accepted and 5 refused Adv
	// create a new configuration with MaxAcceptableAdv = 15
	// with the new configuration, check the 5 refused Adv are accepted
	config := configv1alpha1.ClusterConfig{
		Spec: configv1alpha1.ClusterConfigSpec{
			AdvertisementConfig: configv1alpha1.AdvertisementConfig{
				MaxAcceptableAdvertisement: int32(advCount),
				AutoAccept:                 true,
			},
		},
	}

	// TRUE TEST
	// test the true branch of ManageConfigUpdate
	err, flag := r.ManageConfigUpdate(&config, &advList)
	assert.Nil(t, err)
	assert.True(t, flag)
	assert.Equal(t, config.Spec.AdvertisementConfig, r.ClusterConfig)
	assert.Equal(t, int32(advCount), r.AcceptedAdvNum)
	for _, adv := range advList.Items {
		assert.Equal(t, advcontroller.AdvertisementAccepted, adv.Status.AdvertisementStatus)
	}

	// FALSE TEST
	// apply again the same configuration
	// we enter in the false branch of ManageConfigUpdate but nothing should change
	err, flag = r.ManageConfigUpdate(&config, &advList)
	assert.Nil(t, err)
	assert.False(t, flag)
	assert.Equal(t, config.Spec.AdvertisementConfig, r.ClusterConfig)
	assert.Equal(t, int32(advCount), r.AcceptedAdvNum)

	//TODO: FALSE TEST with config.MaxAcceptableAdvertisement < r.AcceptedAdvNum
	//      cannot test it yet (it needs a client)
}
