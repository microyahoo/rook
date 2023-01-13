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

// Code generated by informer-gen. DO NOT EDIT.

package v1alpha1

import (
	"context"
	time "time"

	objectbucketiov1alpha1 "github.com/kube-object-storage/lib-bucket-provisioner/pkg/apis/objectbucket.io/v1alpha1"
	versioned "github.com/kube-object-storage/lib-bucket-provisioner/pkg/client/clientset/versioned"
	internalinterfaces "github.com/kube-object-storage/lib-bucket-provisioner/pkg/client/informers/externalversions/internalinterfaces"
	v1alpha1 "github.com/kube-object-storage/lib-bucket-provisioner/pkg/client/listers/objectbucket.io/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// ObjectBucketInformer provides access to a shared informer and lister for
// ObjectBuckets.
type ObjectBucketInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() v1alpha1.ObjectBucketLister
}

type objectBucketInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
}

// NewObjectBucketInformer constructs a new informer for ObjectBucket type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewObjectBucketInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredObjectBucketInformer(client, resyncPeriod, indexers, nil)
}

// NewFilteredObjectBucketInformer constructs a new informer for ObjectBucket type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredObjectBucketInformer(client versioned.Interface, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options v1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ObjectbucketV1alpha1().ObjectBuckets().List(context.TODO(), options)
			},
			WatchFunc: func(options v1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ObjectbucketV1alpha1().ObjectBuckets().Watch(context.TODO(), options)
			},
		},
		&objectbucketiov1alpha1.ObjectBucket{},
		resyncPeriod,
		indexers,
	)
}

func (f *objectBucketInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredObjectBucketInformer(client, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *objectBucketInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&objectbucketiov1alpha1.ObjectBucket{}, f.defaultInformer)
}

func (f *objectBucketInformer) Lister() v1alpha1.ObjectBucketLister {
	return v1alpha1.NewObjectBucketLister(f.Informer().GetIndexer())
}
