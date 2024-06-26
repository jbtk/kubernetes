/*
Copyright 2023 The Kubernetes Authors.

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

package consistencydetector

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/google/go-cmp/cmp"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
)

type RetrieveItemsFunc[U any] func() []U

type ListFunc[T runtime.Object] func(ctx context.Context, options metav1.ListOptions) (T, error)

// CheckDataConsistency exists solely for testing purposes.
// we cannot use checkWatchListDataConsistencyIfRequested because
// it is guarded by an environmental variable.
// we cannot manipulate the environmental variable because
// it will affect other tests in this package.
func CheckDataConsistency[T runtime.Object, U any](ctx context.Context, identity string, lastSyncedResourceVersion string, listFn ListFunc[T], listOptions metav1.ListOptions, retrieveItemsFn RetrieveItemsFunc[U]) {
	klog.Warningf("data consistency check for %s is enabled, this will result in an additional call to the API server.", identity)
	listOptions.ResourceVersion = lastSyncedResourceVersion
	listOptions.ResourceVersionMatch = metav1.ResourceVersionMatchExact
	var list runtime.Object
	err := wait.PollUntilContextCancel(ctx, time.Second, true, func(_ context.Context) (done bool, err error) {
		list, err = listFn(ctx, listOptions)
		if err != nil {
			// the consistency check will only be enabled in the CI
			// and LIST calls in general will be retired by the client-go library
			// if we fail simply log and retry
			klog.Errorf("failed to list data from the server, retrying until stopCh is closed, err: %v", err)
			return false, nil
		}
		return true, nil
	})
	if err != nil {
		klog.Errorf("failed to list data from the server, the data consistency check for %s won't be performed, stopCh was closed, err: %v", identity, err)
		return
	}

	rawListItems, err := meta.ExtractListWithAlloc(list)
	if err != nil {
		panic(err) // this should never happen
	}

	listItems := toMetaObjectSliceOrDie(rawListItems)
	retrievedItems := toMetaObjectSliceOrDie(retrieveItemsFn())

	sort.Sort(byUID(listItems))
	sort.Sort(byUID(retrievedItems))

	if !cmp.Equal(listItems, retrievedItems) {
		klog.Infof("previously received data for %s is different than received by the standard list api call against etcd, diff: %v", identity, cmp.Diff(listItems, retrievedItems))
		msg := fmt.Sprintf("data inconsistency detected for %s, panicking!", identity)
		panic(msg)
	}
}

type byUID []metav1.Object

func (a byUID) Len() int           { return len(a) }
func (a byUID) Less(i, j int) bool { return a[i].GetUID() < a[j].GetUID() }
func (a byUID) Swap(i, j int)      { a[i], a[j] = a[j], a[i] }

func toMetaObjectSliceOrDie[T any](s []T) []metav1.Object {
	result := make([]metav1.Object, len(s))
	for i, v := range s {
		m, err := meta.Accessor(v)
		if err != nil {
			panic(err)
		}
		result[i] = m
	}
	return result
}
