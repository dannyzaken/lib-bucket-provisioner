package util

import (
	"context"
	"fmt"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"k8s.io/klog"

	"k8s.io/apimachinery/pkg/types"

	"k8s.io/api/core/v1"
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/yard-turkey/lib-bucket-provisioner/pkg/apis/objectbucket.io/v1alpha1"
)

const (
	DefaultRetryBaseInterval = time.Second * 10
	DefaultRetryTimeout      = time.Second * 360
	DefaultRetryBackOff      = 1

	BucketName      = "BUCKET_NAME"
	BucketHost      = "BUCKET_HOST"
	BucketPort      = "BUCKET_PORT"
	BucketRegion    = "BUCKET_REGION"
	BucketSubRegion = "BUCKET_SUBREGION"
	BucketURL       = "BUCKET_URL"
	BucketSSL       = "BUCKET_SSL"

	DebugLogLvl = 2

	DomainPrefix = "objectbucket.io"
	Finalizer    = DomainPrefix + "/finalizer"
)

func StorageClassForClaim(obc *v1alpha1.ObjectBucketClaim, client client.Client, ctx context.Context) (*storagev1.StorageClass, error) {

	if obc == nil {
		return nil, fmt.Errorf("got nil ObjectBucketClaim ptr")
	}

	if obc.Spec.StorageClassName == "" {
		// TODO (copejon) ignore undefined storage classes to future proofing of static binding
		klog.Warningf("no StorageClass defined for ObjectBucketClaim \"%s/%s\"", obc.Namespace, obc.Name)
		return nil, nil
	}

	class := &storagev1.StorageClass{}
	err := client.Get(
		ctx,
		types.NamespacedName{
			Namespace: "",
			Name:      obc.Spec.StorageClassName,
		},
		class)
	if err != nil {
		return nil, fmt.Errorf("error getting storage class %q: %v", obc.Spec.StorageClassName, err)
	}
	return class, nil
}

// NewCredentailsSecret returns a secret with data appropriate to the supported authenticaion method
// Right now, this is just access keys
func NewCredentialsSecret(obc *v1alpha1.ObjectBucketClaim, auth *v1alpha1.Authentication) (*v1.Secret, error) {

	if obc == nil {
		return nil, fmt.Errorf("ObjectBucketClaim required to generate secret")
	}
	if auth == nil {
		return nil, fmt.Errorf("ObjectBucket required to generate secret")
	}

	klog.V(DebugLogLvl).Infof("generating new secret for ObjectBucketClaim \"%s/%s\"", obc.Namespace, obc.Name)

	secret := &v1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:       obc.Name,
			Namespace:  obc.Namespace,
			Finalizers: []string{Finalizer},
		},
	}

	secret.StringData = auth.ToMap()
	if len(secret.StringData) == 0 {
		// The provisioner may not have provided credentials deliberately, just log a warning
		klog.Warningf("connection authentication not provided for ObjectBucketClaim %s%s", obc.Namespace, obc.Name)
	}
	return secret, nil
}

func NewBucketConfigMap(ep *v1alpha1.Endpoint, obc *v1alpha1.ObjectBucketClaim) (*v1.ConfigMap, error) {

	if ep == nil || obc == nil {
		return nil, fmt.Errorf("v1alpha1.Endpoint and v1alpha1.ObjectbucketClaim cannot be nil")
	}

	var host, bucketPath string
	if obc.Spec.SSL {
		host = "https://" + ep.BucketHost
	} else {
		host = "http://" + ep.BucketHost
	}
	if ep.BucketPort > 0 {
		host = fmt.Sprintf("%s:%d", host, ep.BucketPort)
	}
	bucketPath = path.Join(ep.Region, ep.SubRegion, ep.BucketName)

	bucketURL, err := url.Parse(fmt.Sprintf("%s/%s", host, bucketPath))
	if err != nil {
		fmt.Errorf("error composing bucket url %q: %v", bucketURL, err)
		return nil, fmt.Errorf("malformed bucket url %q: %v", bucketPath, err)
	}

	return &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      obc.Name,
			Namespace: obc.Namespace,
		},
		Data: map[string]string{
			BucketName:      obc.Spec.BucketName,
			BucketHost:      ep.BucketHost,
			BucketPort:      strconv.Itoa(ep.BucketPort),
			BucketSSL:       strconv.FormatBool(ep.SSL),
			BucketRegion:    ep.Region,
			BucketSubRegion: ep.SubRegion,
			BucketURL:       bucketURL.String(),
		},
	}, nil
}

func NewObjectBucket(obc *v1alpha1.ObjectBucketClaim, connection *v1alpha1.Connection) *v1alpha1.ObjectBucket {
	return &v1alpha1.ObjectBucket{
		ObjectMeta: metav1.ObjectMeta{
			Name: fmt.Sprintf("obc-%s-%s", obc.Namespace, obc.Name),
		},
		Spec: v1alpha1.ObjectBucketSpec{
			Connection: connection,
		},
		Status: v1alpha1.ObjectBucketStatus{},
	}
}

func CreateUntilDefaultTimeout(obj runtime.Object, c client.Client) error {

	if c == nil {
		return fmt.Errorf("error creating object, nil client")
	}

	return wait.PollImmediate(DefaultRetryBaseInterval, DefaultRetryTimeout, func() (done bool, err error) {
		err = c.Create(context.Background(), obj)
		if err != nil && !errors.IsAlreadyExists(err) {
			return false, err
		}
		return true, nil
	})
}

func TranslateReclaimPolicy(rp v1.PersistentVolumeReclaimPolicy) (v1alpha1.ReclaimPolicy, error) {
	switch v1alpha1.ReclaimPolicy(strings.ToLower(string(rp))) {
	case v1alpha1.ReclaimPolicyDelete:
		return v1alpha1.ReclaimPolicyDelete, nil
	case v1alpha1.ReclaimPolicyRetain:
		return v1alpha1.ReclaimPolicyRetain, nil
	}
	return "", fmt.Errorf("unrecognized reclaim policy %q", rp)
}

const suffixLen = 5

func GenerateBucketName(prefix string) string {
	suf := rand.String(suffixLen)
	if prefix == "" {
		return suf
	}
	return fmt.Sprintf("%s-%s", prefix, suf)
}
