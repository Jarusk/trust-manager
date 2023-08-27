/*
Copyright 2021 The cert-manager Authors.

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

package bundle

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2/klogr"
	"k8s.io/utils/pointer"
	"sigs.k8s.io/controller-runtime/pkg/client"
	fakeclient "sigs.k8s.io/controller-runtime/pkg/client/fake"

	trustapi "github.com/cert-manager/trust-manager/pkg/apis/trust/v1alpha1"
	"github.com/cert-manager/trust-manager/pkg/fspkg"
	"github.com/cert-manager/trust-manager/test/dummy"

	jks "github.com/pavlo-v-chernykh/keystore-go/v4"
)

func Test_syncTarget(t *testing.T) {
	const (
		bundleName = "test-bundle"
		key        = "trust.pem"
		jksKey     = "trust.jks"
		data       = dummy.TestCertificate1
	)

	labelEverything := func(*testing.T) labels.Selector {
		return labels.Everything()
	}

	tests := map[string]struct {
		object    runtime.Object
		namespace corev1.Namespace
		selector  func(t *testing.T) labels.Selector
		// Add JKS to AdditionalFormats
		withJKS bool
		// Expect the configmap to exist at the end of the sync.
		expExists bool
		// Expect JKS to exist in the configmap at the end of the sync.
		expJKS   bool
		expEvent string
		// Expect the owner reference of the configmap to point to the bundle.
		expOwnerReference bool
		expNeedsUpdate    bool
	}{
		"if object doesn't exist, expect update": {
			object:            nil,
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			expExists:         true,
			expOwnerReference: true,
			expNeedsUpdate:    true,
		},
		"if object doesn't exist with JKS, expect update": {
			object:            nil,
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			withJKS:           true,
			expExists:         true,
			expJKS:            true,
			expOwnerReference: true,
			expNeedsUpdate:    true,
		},
		"if object exists but without data or owner, expect update": {
			object:            &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: bundleName, Namespace: "test-namespace"}},
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			expExists:         true,
			expOwnerReference: true,
			expNeedsUpdate:    true,
		},
		"if object exists with data but no owner, expect update": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: bundleName, Namespace: "test-namespace"},
				Data:       map[string]string{key: data},
			},
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			expExists:         true,
			expOwnerReference: true,
			expNeedsUpdate:    true,
		},
		"if object exists with owner but no data, expect update": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: "test-namespace",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:               "Bundle",
							APIVersion:         "trust.cert-manager.io/v1alpha1",
							Name:               bundleName,
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
			},
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			expExists:         true,
			expOwnerReference: true,
			expNeedsUpdate:    true,
		},
		"if object exists with owner but wrong data, expect update": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: "test-namespace",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:               "Bundle",
							APIVersion:         "trust.cert-manager.io/v1alpha1",
							Name:               bundleName,
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
				Data: map[string]string{key: "wrong data"},
			},
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			expExists:         true,
			expOwnerReference: true,
			expNeedsUpdate:    true,
		},
		"if object exists with owner but without JKS, expect update": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: "test-namespace",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:               "Bundle",
							APIVersion:         "trust.cert-manager.io/v1alpha1",
							Name:               bundleName,
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
				Data: map[string]string{key: data},
			},
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			withJKS:           true,
			expExists:         true,
			expJKS:            true,
			expOwnerReference: true,
			expNeedsUpdate:    true,
		},
		"if object exists with owner but wrong key, expect update": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: "test-namespace",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:               "Bundle",
							APIVersion:         "trust.cert-manager.io/v1alpha1",
							Name:               bundleName,
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
				Data: map[string]string{"wrong key": data},
			},
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			expExists:         true,
			expOwnerReference: true,
			expNeedsUpdate:    true,
		},
		"if object exists with owner but wrong JKS key, expect update": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: "test-namespace",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:               "Bundle",
							APIVersion:         "trust.cert-manager.io/v1alpha1",
							Name:               bundleName,
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
				Data: map[string]string{key: data},
				BinaryData: map[string][]byte{
					"wrong-key": []byte(data),
				},
			},
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			withJKS:           true,
			expExists:         true,
			expJKS:            true,
			expOwnerReference: true,
			expNeedsUpdate:    true,
		},
		"if object exists with correct data, expect no update": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: "test-namespace",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:               "Bundle",
							APIVersion:         "trust.cert-manager.io/v1alpha1",
							Name:               bundleName,
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
				Data: map[string]string{key: data},
			},
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			expExists:         true,
			expOwnerReference: true,
			expNeedsUpdate:    false,
		},
		"if object exists without JKS, expect update": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: "test-namespace",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:               "Bundle",
							APIVersion:         "trust.cert-manager.io/v1alpha1",
							Name:               bundleName,
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
				Data: map[string]string{key: data},
			},
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			withJKS:           true,
			expExists:         true,
			expJKS:            true,
			expOwnerReference: true,
			expNeedsUpdate:    true,
		},
		"if object exists with correct data and some extra data and owner, expect no update": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: "test-namespace",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:               "Bundle",
							APIVersion:         "trust.cert-manager.io/v1alpha1",
							Name:               bundleName,
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
						{
							Kind:               "Bundle",
							APIVersion:         "trust.cert-manager.io/v1alpha1",
							Name:               "another-bundle",
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
				Data: map[string]string{key: data, "another-key": "another-data"},
			},
			namespace:         corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "test-namespace"}},
			selector:          labelEverything,
			expExists:         true,
			expOwnerReference: true,
			expNeedsUpdate:    false,
		},
		"if object doesn't exist and labels match, expect update": {
			object: nil,
			namespace: corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   "test-namespace",
				Labels: map[string]string{"foo": "bar"},
			}},
			selector: func(t *testing.T) labels.Selector {
				req, err := labels.NewRequirement("foo", selection.Equals, []string{"bar"})
				assert.NoError(t, err)
				return labels.NewSelector().Add(*req)
			},
			expExists:         true,
			expOwnerReference: true,
			expNeedsUpdate:    true,
		},
		"if object doesn't exist and labels don't match, don't expect update": {
			object: nil,
			namespace: corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   "test-namespace",
				Labels: map[string]string{"bar": "foo"},
			}},
			selector: func(t *testing.T) labels.Selector {
				req, err := labels.NewRequirement("foo", selection.Equals, []string{"bar"})
				assert.NoError(t, err)
				return labels.NewSelector().Add(*req)
			},
			expExists:         false,
			expOwnerReference: true,
			expNeedsUpdate:    false,
		},
		"if object exists with correct data and labels match, expect no update": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: "test-namespace",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:               "Bundle",
							APIVersion:         "trust.cert-manager.io/v1alpha1",
							Name:               bundleName,
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
				Data: map[string]string{key: data},
			},
			namespace: corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   "test-namespace",
				Labels: map[string]string{"foo": "bar"},
			}},
			selector: func(t *testing.T) labels.Selector {
				req, err := labels.NewRequirement("foo", selection.Equals, []string{"bar"})
				assert.NoError(t, err)
				return labels.NewSelector().Add(*req)
			},
			expExists:         true,
			expOwnerReference: true,
			expNeedsUpdate:    false,
		},
		"if object exists with correct data but labels don't match, expect deletion": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: "test-namespace",
					OwnerReferences: []metav1.OwnerReference{
						{
							Kind:               "Bundle",
							APIVersion:         "trust.cert-manager.io/v1alpha1",
							Name:               bundleName,
							Controller:         pointer.Bool(true),
							BlockOwnerDeletion: pointer.Bool(true),
						},
					},
				},
				Data: map[string]string{key: data},
			},
			namespace: corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   "test-namespace",
				Labels: map[string]string{"bar": "foo"},
			}},
			selector: func(t *testing.T) labels.Selector {
				req, err := labels.NewRequirement("foo", selection.Equals, []string{"bar"})
				assert.NoError(t, err)
				return labels.NewSelector().Add(*req)
			},
			expExists:         false,
			expOwnerReference: false,
			expNeedsUpdate:    true,
		},
		"if object exists and labels don't match, but controller doesn't have ownership, expect no update": {
			object: &corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{
					Name:      bundleName,
					Namespace: "test-namespace",
				},
				Data: map[string]string{key: data},
			},
			namespace: corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name:   "test-namespace",
				Labels: map[string]string{"bar": "foo"},
			}},
			selector: func(t *testing.T) labels.Selector {
				req, err := labels.NewRequirement("foo", selection.Equals, []string{"bar"})
				assert.NoError(t, err)
				return labels.NewSelector().Add(*req)
			},
			expExists:         true,
			expOwnerReference: false,
			expNeedsUpdate:    false,
			expEvent:          "Warning NotOwned ConfigMap is not owned by trust.cert-manager.io so ignoring",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			clientBuilder := fakeclient.NewClientBuilder().WithScheme(trustapi.GlobalScheme)
			if test.object != nil {
				clientBuilder.WithRuntimeObjects(test.object)
			}

			fakeclient := clientBuilder.Build()
			fakerecorder := record.NewFakeRecorder(1)

			b := &bundle{targetDirectClient: fakeclient, recorder: fakerecorder}

			spec := trustapi.BundleSpec{Target: trustapi.BundleTarget{ConfigMap: &trustapi.KeySelector{Key: key}}}
			if test.withJKS {
				spec.Target.AdditionalFormats = &trustapi.AdditionalFormats{JKS: &trustapi.KeySelector{Key: jksKey}}
			}

			needsUpdate, err := b.syncTarget(context.TODO(), klogr.New(), &trustapi.Bundle{
				ObjectMeta: metav1.ObjectMeta{Name: bundleName},
				Spec:       spec,
			}, test.selector(t), &test.namespace, data)
			assert.NoError(t, err)

			assert.Equalf(t, test.expNeedsUpdate, needsUpdate, "unexpected needsUpdate, exp=%t got=%t", test.expNeedsUpdate, needsUpdate)

			var configMap corev1.ConfigMap
			err = fakeclient.Get(context.TODO(), client.ObjectKey{Namespace: test.namespace.Name, Name: bundleName}, &configMap)
			assert.Equalf(t, test.expExists, !apierrors.IsNotFound(err), "unexpected is not found: %v", err)

			if test.expExists {
				assert.Equalf(t, data, configMap.Data[key], "unexpected data on ConfigMap: exp=%s:%s got=%v", key, data, configMap.Data)

				expectedOwnerReference := metav1.OwnerReference{
					Kind:               "Bundle",
					APIVersion:         "trust.cert-manager.io/v1alpha1",
					Name:               bundleName,
					Controller:         pointer.Bool(true),
					BlockOwnerDeletion: pointer.Bool(true),
				}
				if test.expOwnerReference {
					assert.Equalf(t, expectedOwnerReference, configMap.OwnerReferences[0], "unexpected data on ConfigMap: exp=%s:%s got=%v", key, data, configMap.Data)
				} else {
					assert.NotContains(t, configMap.OwnerReferences, expectedOwnerReference)
				}

				jksData, jksExists := configMap.BinaryData[jksKey]
				assert.Equal(t, test.expJKS, jksExists)

				if test.expJKS {
					reader := bytes.NewReader(jksData)

					ks := jks.New()
					err := ks.Load(reader, []byte(DefaultJKSPassword))
					assert.Nil(t, err)

					entryNames := ks.Aliases()

					assert.Len(t, entryNames, 1)
					assert.True(t, ks.IsTrustedCertificateEntry(entryNames[0]))

					// Safe to ignore errors here, we've tested that it's present and a TrustedCertificateEntry
					cert, _ := ks.GetTrustedCertificateEntry(entryNames[0])

					// Only one certificate block for this test, so we can safely ignore the `remaining` byte array
					p, _ := pem.Decode([]byte(data))
					assert.Equal(t, p.Bytes, cert.Certificate.Content)
				}
			}

			var event string
			select {
			case event = <-fakerecorder.Events:
			default:
			}
			assert.Equal(t, test.expEvent, event)
		})
	}
}

func Test_buildSourceBundle(t *testing.T) {
	tests := map[string]struct {
		bundle           *trustapi.Bundle
		objects          []runtime.Object
		expData          string
		expError         bool
		expNotFoundError bool
	}{
		"if no sources defined, should return an error": {
			bundle:           &trustapi.Bundle{},
			objects:          []runtime.Object{},
			expData:          "",
			expError:         true,
			expNotFoundError: false,
		},
		"if single InLine source defined with newlines, should trim and return": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{InLine: pointer.String(dummy.TestCertificate1 + "\n" + dummy.TestCertificate2 + "\n\n")},
			}}},
			objects:          []runtime.Object{},
			expData:          dummy.JoinCerts(dummy.TestCertificate1, dummy.TestCertificate2),
			expError:         false,
			expNotFoundError: false,
		},
		"if single DefaultPackage source defined, should return": {
			bundle:           &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{{UseDefaultCAs: pointer.Bool(true)}}}},
			objects:          []runtime.Object{},
			expData:          dummy.JoinCerts(dummy.TestCertificate5),
			expError:         false,
			expNotFoundError: false,
		},
		"if single ConfigMap source which doesn't exist, return notFoundError": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{ConfigMap: &trustapi.SourceObjectKeySelector{Name: "configmap", KeySelector: trustapi.KeySelector{Key: "key"}}},
			}}},
			objects:          []runtime.Object{},
			expData:          "",
			expError:         true,
			expNotFoundError: true,
		},
		"if single ConfigMap source whose key doesn't exist, return notFoundError": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{ConfigMap: &trustapi.SourceObjectKeySelector{Name: "configmap", KeySelector: trustapi.KeySelector{Key: "key"}}},
			}}},
			objects:          []runtime.Object{&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "configmap"}}},
			expData:          "",
			expError:         true,
			expNotFoundError: true,
		},
		"if single ConfigMap source, return data": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{ConfigMap: &trustapi.SourceObjectKeySelector{Name: "configmap", KeySelector: trustapi.KeySelector{Key: "key"}}},
			}}},
			objects: []runtime.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "configmap"},
				Data:       map[string]string{"key": dummy.TestCertificate1 + "\n" + dummy.TestCertificate2},
			}},
			expData:          dummy.JoinCerts(dummy.TestCertificate1, dummy.TestCertificate2),
			expError:         false,
			expNotFoundError: false,
		},
		"if ConfigMap and InLine source, return concatenated data": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{ConfigMap: &trustapi.SourceObjectKeySelector{Name: "configmap", KeySelector: trustapi.KeySelector{Key: "key"}}},
				{InLine: pointer.String(dummy.TestCertificate2)},
			}}},
			objects: []runtime.Object{&corev1.ConfigMap{
				ObjectMeta: metav1.ObjectMeta{Name: "configmap"},
				Data:       map[string]string{"key": dummy.TestCertificate1},
			}},
			expData:          dummy.JoinCerts(dummy.TestCertificate1, dummy.TestCertificate2),
			expError:         false,
			expNotFoundError: false,
		},
		"if single Secret source exists which doesn't exist, should return not found error": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{Secret: &trustapi.SourceObjectKeySelector{Name: "secret", KeySelector: trustapi.KeySelector{Key: "key"}}},
			}}},
			objects:          []runtime.Object{},
			expData:          "",
			expError:         true,
			expNotFoundError: true,
		},
		"if single Secret source whose key doesn't exist, return notFoundError": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{Secret: &trustapi.SourceObjectKeySelector{Name: "secret", KeySelector: trustapi.KeySelector{Key: "key"}}},
			}}},
			objects:          []runtime.Object{&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "secret"}}},
			expData:          "",
			expError:         true,
			expNotFoundError: true,
		},
		"if single Secret source, return data": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{Secret: &trustapi.SourceObjectKeySelector{Name: "secret", KeySelector: trustapi.KeySelector{Key: "key"}}},
			}}},
			objects: []runtime.Object{&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "secret"},
				Data:       map[string][]byte{"key": []byte(dummy.TestCertificate1 + "\n" + dummy.TestCertificate2)},
			}},
			expData:          dummy.JoinCerts(dummy.TestCertificate1, dummy.TestCertificate2),
			expError:         false,
			expNotFoundError: false,
		},
		"if Secret and InLine source, return concatenated data": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{Secret: &trustapi.SourceObjectKeySelector{Name: "secret", KeySelector: trustapi.KeySelector{Key: "key"}}},
				{InLine: pointer.String(dummy.TestCertificate1)},
			}}},
			objects: []runtime.Object{&corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Name: "secret"},
				Data:       map[string][]byte{"key": []byte(dummy.TestCertificate2)},
			}},
			expData:          dummy.JoinCerts(dummy.TestCertificate2, dummy.TestCertificate1),
			expError:         false,
			expNotFoundError: false,
		},
		"if Secret, ConfigmMap and InLine source, return concatenated data": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{ConfigMap: &trustapi.SourceObjectKeySelector{Name: "configmap", KeySelector: trustapi.KeySelector{Key: "key"}}},
				{InLine: pointer.String(dummy.TestCertificate3)},
				{Secret: &trustapi.SourceObjectKeySelector{Name: "secret", KeySelector: trustapi.KeySelector{Key: "key"}}},
			}}},
			objects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "configmap"},
					Data:       map[string]string{"key": dummy.TestCertificate1},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "secret"},
					Data:       map[string][]byte{"key": []byte(dummy.TestCertificate2)},
				},
			},
			expData:          dummy.JoinCerts(dummy.TestCertificate1, dummy.TestCertificate3, dummy.TestCertificate2),
			expError:         false,
			expNotFoundError: false,
		},
		"if source Secret exists, but not ConfigMap, return not found error": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{ConfigMap: &trustapi.SourceObjectKeySelector{Name: "configmap", KeySelector: trustapi.KeySelector{Key: "key"}}},
				{Secret: &trustapi.SourceObjectKeySelector{Name: "secret", KeySelector: trustapi.KeySelector{Key: "key"}}},
			}}},
			objects: []runtime.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "configmap"},
					Data:       map[string]string{"key": dummy.TestCertificate1},
				},
			},
			expData:          "",
			expError:         true,
			expNotFoundError: true,
		},
		"if source ConfigMap exists, but not Secret, return not found error": {
			bundle: &trustapi.Bundle{Spec: trustapi.BundleSpec{Sources: []trustapi.BundleSource{
				{ConfigMap: &trustapi.SourceObjectKeySelector{Name: "configmap", KeySelector: trustapi.KeySelector{Key: "key"}}},
				{Secret: &trustapi.SourceObjectKeySelector{Name: "secret", KeySelector: trustapi.KeySelector{Key: "key"}}},
			}}},
			objects: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{Name: "secret"},
					Data:       map[string][]byte{"key": []byte(dummy.TestCertificate1)},
				},
			},
			expData:          "",
			expError:         true,
			expNotFoundError: true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			fakeclient := fakeclient.NewClientBuilder().
				WithRuntimeObjects(test.objects...).
				WithScheme(trustapi.GlobalScheme).
				Build()

			b := &bundle{
				targetDirectClient: fakeclient,
				sourceLister:       fakeclient,
				defaultPackage: &fspkg.Package{
					Name:    "testpkg",
					Version: "123",
					Bundle:  dummy.TestCertificate5,
				},
			}

			resolvedBundle, err := b.buildSourceBundle(context.TODO(), test.bundle)

			if (err != nil) != test.expError {
				t.Errorf("unexpected error, exp=%t got=%v", test.expError, err)
			}
			if errors.As(err, &notFoundError{}) != test.expNotFoundError {
				t.Errorf("unexpected notFoundError, exp=%t got=%v", test.expNotFoundError, err)
			}

			if resolvedBundle.data != test.expData {
				t.Errorf("unexpected data, exp=%q got=%q", test.expData, resolvedBundle.data)
			}
		})
	}
}

func Test_encodeJKSAliases(t *testing.T) {
	// IMPORTANT: We use TestCertificate1 and TestCertificate2 here because they're defined
	// to be self-signed and to also use the same Subject, while being different certs.
	// This test ensures that the aliases we create when adding to a JKS file is different under
	// these conditions (where the issuer / subject is identical).
	// Using different dummy certs would allow this test to pass but wouldn't actually test anything useful!
	bundle := dummy.JoinCerts(dummy.TestCertificate1, dummy.TestCertificate2)
	certs, err := parsePem(bundle)
	if err != nil {
		t.Fatalf("failed to load dummy data: %s", err)
	}

	password := []byte(DefaultJKSPassword)

	jksFile, err := encodeJKS(certs, password)
	if err != nil {
		t.Fatalf("didn't expect an error but got: %s", err)
	}

	reader := bytes.NewReader(jksFile)

	ks := jks.New()

	err = ks.Load(reader, password)
	if err != nil {
		t.Fatalf("failed to parse generated JKS file: %s", err)
	}

	entryNames := ks.Aliases()

	if len(entryNames) != 2 {
		t.Fatalf("expected two certs in JKS file but got %d", len(entryNames))
	}
}

func Test_jksAlias(t *testing.T) {
	// We might not ever rely on aliases being stable, but this test seeks
	// to enforce stability for now. It'll be easy to remove.

	// If this test starts failing after TestCertificate1 is updated, it'll
	// need to be updated with the new alias for the new cert.

	block, _ := pem.Decode([]byte(dummy.TestCertificate1))
	if block == nil {
		t.Fatalf("couldn't parse a PEM block from TestCertificate1")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("Dummy certificate TestCertificate1 couldn't be parsed: %s", err)
	}

	alias := keystoreAlias(cert.Raw, cert.Subject.String())

	expectedAlias := "548b988f|CN=cmct-test-root,O=cert-manager"

	if alias != expectedAlias {
		t.Fatalf("expected alias to be %q but got %q", expectedAlias, alias)
	}
}
