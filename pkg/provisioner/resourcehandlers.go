/*
Copyright 2019 Red Hat Inc.

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

package provisioner

import (
	"fmt"
	"strconv"
	"time"

	"k8s.io/client-go/kubernetes"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kube-object-storage/lib-bucket-provisioner/pkg/apis/objectbucket.io/v1alpha1"
	"github.com/kube-object-storage/lib-bucket-provisioner/pkg/client/clientset/versioned"
	"github.com/kube-object-storage/lib-bucket-provisioner/pkg/provisioner/api"
)

const (
	// defaultRetryBaseInterval controls how long to wait for a single create API object call
	defaultRetryBaseInterval = time.Second * 3
	// defaultRetryTimeout defines how long in total to try to create an API object before ending the reconciliation
	// attempt
	defaultRetryTimeout = time.Second * 30

	bucketName      = "BUCKET_NAME"
	bucketHost      = "BUCKET_HOST"
	bucketPort      = "BUCKET_PORT"
	bucketRegion    = "BUCKET_REGION"
	bucketSubRegion = "BUCKET_SUBREGION"
	bucketSSL       = "BUCKET_SSL"

	// finalizer is applied to all resources generated by the provisioner
	finalizer = api.Domain + "/finalizer"

	objectBucketNameFormat = "obc-%s-%s"
)

// newBucketConfigMap returns a config map from a given endpoint and ObjectBucketClaim.
// A finalizer is added to reduce chances of the CM being accidentally deleted. An OwnerReference
// is added so that the CM is automatically garbage collected when the parent OBC is deleted.
func newBucketConfigMap(ep *v1alpha1.Endpoint, obc *v1alpha1.ObjectBucketClaim) (*corev1.ConfigMap, error) {
	if ep == nil {
		return nil, fmt.Errorf("cannot construct configMap, got nil Endpoint")
	}
	if obc == nil {
		return nil, fmt.Errorf("cannot construct configMap, got nil OBC")
	}

	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:       obc.Name,
			Namespace:  obc.Namespace,
			Finalizers: []string{finalizer},
			OwnerReferences: []metav1.OwnerReference{
				makeOwnerReference(obc),
			},
		},
		Data: map[string]string{
			bucketName:      ep.BucketName,
			bucketHost:      ep.BucketHost,
			bucketPort:      strconv.Itoa(ep.BucketPort),
			bucketSSL:       strconv.FormatBool(ep.SSL),
			bucketRegion:    ep.Region,
			bucketSubRegion: ep.SubRegion,
		},
	}, nil
}

// newCredentialsSecret returns a secret with data appropriate to the supported authenticaion
// method. Even if the values for the Authentication keys are empty, we generate the secret.
// A finalizer is added to reduce chances of the secret being accidentally deleted.
// An OwnerReference is added so that the secret is automatically garbage collected when the
// parent OBC is deleted.
func newCredentialsSecret(obc *v1alpha1.ObjectBucketClaim, auth *v1alpha1.Authentication) (*corev1.Secret, error) {

	if obc == nil {
		return nil, fmt.Errorf("ObjectBucketClaim required to generate secret")
	}
	if auth == nil {
		return nil, fmt.Errorf("got nil authentication, nothing to do")
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:       obc.Name,
			Namespace:  obc.Namespace,
			Finalizers: []string{finalizer},
			OwnerReferences: []metav1.OwnerReference{
				makeOwnerReference(obc),
			},
		},
	}

	secret.StringData = auth.ToMap()
	return secret, nil
}

// createObjectBucket creates an OB based on the passed-in ob spec.
// Note: a finalizer has been added to reduce chances of the ob being accidentally deleted.
func createObjectBucket(ob *v1alpha1.ObjectBucket, c versioned.Interface, retryInterval, retryTimeout time.Duration) (result *v1alpha1.ObjectBucket, err error) {
	logD.Info("creating ObjectBucket", "name", ob.Name)

	err = wait.PollImmediate(retryInterval, retryTimeout, func() (bool, error) {
		result, err = c.ObjectbucketV1alpha1().ObjectBuckets().Create(ob)
		if errors.IsAlreadyExists(err) {
			err = nil
		} else if err != nil {
			// could be intermittent api error
			log.Error(err, "probably not fatal, retrying")
		}
		return (err == nil), err
	})
	return
}

func createSecret(obc *v1alpha1.ObjectBucketClaim, auth *v1alpha1.Authentication, c kubernetes.Interface, retryInterval, retryTimeout time.Duration) (*corev1.Secret, error) {
	secret, err := newCredentialsSecret(obc, auth)
	if err != nil {
		return nil, err
	}
	logD.Info("creating Secret", "name", secret.Namespace+"/"+secret.Name)
	err = wait.PollImmediate(retryInterval, retryTimeout, func() (done bool, err error) {
		secret, err = c.CoreV1().Secrets(obc.Namespace).Create(secret)
		if err != nil {
			if errors.IsAlreadyExists(err) {
				// The object already exists don't spam the logs, instead let the request be requeued
				return true, err
			}
			// The error could be intermittent, log and try again
			log.Error(err, "probably not fatal, retrying")
			return false, nil
		}
		return true, nil
	})
	return secret, err
}

func createConfigMap(obc *v1alpha1.ObjectBucketClaim, ep *v1alpha1.Endpoint, c kubernetes.Interface, retryInterval, retryTimeout time.Duration) (*corev1.ConfigMap, error) {
	configMap, err := newBucketConfigMap(ep, obc)
	if err != nil {
		return nil, err
	}

	logD.Info("creating ConfigMap", "name", configMap.Namespace+"/"+configMap.Name)
	err = wait.PollImmediate(retryInterval, retryTimeout, func() (done bool, err error) {
		configMap, err = c.CoreV1().ConfigMaps(obc.Namespace).Create(configMap)
		if err != nil {
			if errors.IsAlreadyExists(err) {
				// The object already exists don't spam the logs, instead let the request be requeued
				return true, err
			}
			// The error could be intermittent, log and try again
			log.Error(err, "probably not fatal, retrying")
			return false, nil
		}
		return true, nil
	})
	return configMap, err
}

// Only the finalizer needs to be removed. The CM will be garbage collected since its
// ownerReference refers to the parent OBC.
func releaseConfigMap(cm *corev1.ConfigMap, c kubernetes.Interface) (err error) {
	if cm == nil {
		logD.Info("got nil configmap, skipping")
		return nil
	}
	cm, err = c.CoreV1().ConfigMaps(cm.Namespace).Get(cm.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	logD.Info("removing configmap finalizer")
	removeFinalizer(cm)
	cm, err = c.CoreV1().ConfigMaps(cm.Namespace).Update(cm)
	if err != nil {
		return err
	}

	return nil
}

// Only the finalizer needs to be removed. The Secret will be garbage collected since its
// ownerReference refers to the parent OBC.
func releaseSecret(sec *corev1.Secret, c kubernetes.Interface) (err error) {
	if sec == nil {
		logD.Info("got nil secret, skipping")
		return nil
	}
	sec, err = c.CoreV1().Secrets(sec.Namespace).Get(sec.Name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	logD.Info("removing secret finalizer")
	removeFinalizer(sec)
	sec, err = c.CoreV1().Secrets(sec.Namespace).Update(sec)
	if err != nil {
		return err
	}

	return nil
}

// Remove the finalizer allowing the OBC to finally be deleted.
func releaseOBC(obc *v1alpha1.ObjectBucketClaim, c versioned.Interface) (err error) {
	if obc == nil {
		logD.Info("got nil obc, skipping")
		return nil
	}
	obcNsName := obc.Namespace + "/" + obc.Name
	obc, err = c.ObjectbucketV1alpha1().ObjectBucketClaims(obc.Namespace).Get(obc.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("unable to Get obc %q in order to remove finalizer: %v", obcNsName, err)
	}
	logD.Info("removing obc finalizer")
	removeFinalizer(obc)

	obc, err = c.ObjectbucketV1alpha1().ObjectBucketClaims(obc.Namespace).Update(obc)
	if err != nil {
		return fmt.Errorf("unable to Update obc %q to reflect removed finalizer: %v", obcNsName, err)
	}

	return nil
}

// The OB does not have an ownerReference and must be explicitly deleted after its
// finalizer is removed.
// Uses Update() because Patch Strategies are not supported for CRDs
// https://github.com/kubernetes/kubernetes/issues/50037
func deleteObjectBucket(ob *v1alpha1.ObjectBucket, c versioned.Interface) error {
	if ob == nil {
		return nil
	}

	logD.Info("removing ObjectBucket finalizer", "name", ob.Name)
	removeFinalizer(ob)
	ob, err := c.ObjectbucketV1alpha1().ObjectBuckets().Update(ob)
	if err != nil {
		return err
	}

	logD.Info("deleting ObjectBucket", "name", ob.Name)
	err = c.ObjectbucketV1alpha1().ObjectBuckets().Delete(ob.Name, &metav1.DeleteOptions{})
	if err != nil {
		if errors.IsNotFound(err) {
			log.Error(err, "ObjectBucket vanished before we could delete it, skipping", "name", ob.Name)
			return nil
		}
		return fmt.Errorf("error deleting ObjectBucket %q: %v", ob.Name, err)
	}
	logD.Info("ObjectBucket deleted", "name", ob.Name)
	return nil
}

func updateClaim(c versioned.Interface, obc *v1alpha1.ObjectBucketClaim, retryInterval, retryTimeout time.Duration) (result *v1alpha1.ObjectBucketClaim, err error) {

	logD.Info("updating", "obc", obc.Namespace+"/"+obc.Name)
	err = wait.PollImmediate(retryInterval, retryTimeout, func() (bool, error) {
		result, err = c.ObjectbucketV1alpha1().ObjectBucketClaims(obc.Namespace).Update(obc)
		return (err == nil), err
	})
	return
}

func updateObjectBucketClaimPhase(c versioned.Interface, obc *v1alpha1.ObjectBucketClaim, phase v1alpha1.ObjectBucketClaimStatusPhase, retryInterval, retryTimeout time.Duration) (result *v1alpha1.ObjectBucketClaim, err error) {
	logD.Info("updating status:", "obc", obc.Namespace+"/"+obc.Name, "old status",
		obc.Status.Phase, "new status", phase)
	obc.Status.Phase = phase

	err = wait.PollImmediate(retryInterval, retryTimeout, func() (bool, error) {
		result, err = c.ObjectbucketV1alpha1().ObjectBucketClaims(obc.Namespace).UpdateStatus(obc)
		return (err == nil), err
	})
	return
}

func updateObjectBucketPhase(c versioned.Interface, ob *v1alpha1.ObjectBucket, phase v1alpha1.ObjectBucketStatusPhase, retryInterval, retryTimeout time.Duration) (result *v1alpha1.ObjectBucket, err error) {
	logD.Info("updating status:", "ob", ob.Name, "old status", ob.Status.Phase,
		"new status", phase)
	ob.Status.Phase = phase

	err = wait.PollImmediate(retryInterval, retryTimeout, func() (bool, error) {
		result, err = c.ObjectbucketV1alpha1().ObjectBuckets().UpdateStatus(ob)
		return (err == nil), err
	})
	return
}
