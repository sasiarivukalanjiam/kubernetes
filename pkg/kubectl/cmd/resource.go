/*
Copyright 2014 Google Inc. All rights reserved.

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

package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/meta"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/api/validation"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/kubectl"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/labels"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/runtime"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util"
	"github.com/golang/glog"
	"github.com/spf13/cobra"
)

// Resource defines interface for resources
type Resource interface {
	Delete(io.Writer) error
	Get(io.Writer) (runtime.Object, error)
}

// ResourceInfo contains temporary info to execute REST call
type ResourceInfo struct {
	Client    kubectl.RESTClient
	Mapping   *meta.RESTMapping
	Namespace string
	Name      string
}

func NewResourceInfo(client kubectl.RESTClient, mapping *meta.RESTMapping, namespace, name string) *ResourceInfo {
	return &ResourceInfo{
		Client:    client,
		Mapping:   mapping,
		Namespace: namespace,
		Name:      name,
	}
}

func (r *ResourceInfo) Delete(out io.Writer) error {
	err := kubectl.NewRESTHelper(r.Client, r.Mapping).Delete(r.Namespace, r.Name)
	if err == nil {
		fmt.Fprintf(out, "%s\n", r.Name)
	}
	return err
}

func (r *ResourceInfo) Get(out io.Writer) (runtime.Object, error) {
	var labelSelector labels.Selector = nil
	return kubectl.NewRESTHelper(r.Client, r.Mapping).Get(r.Namespace, r.Name, labelSelector)
}

// ResourceSelector is a facade for all the resources fetched via label selector
type ResourceSelector struct {
	Client    kubectl.RESTClient
	Mapping   *meta.RESTMapping
	Namespace string
	Selector  labels.Selector
}

func NewResourceSelector(client kubectl.RESTClient, mapping *meta.RESTMapping, namespace string, selector labels.Selector) *ResourceSelector {
	return &ResourceSelector{
		Client:    client,
		Mapping:   mapping,
		Namespace: namespace,
		Selector:  selector,
	}
}

func (r *ResourceSelector) Delete(out io.Writer) error {
	obj, err := r.Get(out)
	if err != nil {
		return err
	}
	objs, _ := runtime.ExtractList(obj)
	for _, o := range objs {
		name, err := r.Mapping.MetadataAccessor.Name(o)
		if err == nil && name != "" {
			err = NewResourceInfo(r.Client, r.Mapping, r.Namespace, name).Delete(out)
			if err != nil {
				glog.Errorf("Unable to delete resource %s", name)
			}
		}
	}
	return nil
}

func (r *ResourceSelector) Get(out io.Writer) (runtime.Object, error) {
	return kubectl.NewRESTHelper(r.Client, r.Mapping).List(r.Namespace, r.Selector)
}

// ResourcesFromArgsOrFile: compute a list of of Resources
// extracting info from filename or  args
func ResourcesFromArgsOrFile(cmd *cobra.Command, args []string, filename, selector string, typer runtime.ObjectTyper, mapper meta.RESTMapper, clientBuilder func(cmd *cobra.Command, mapping *meta.RESTMapping) (kubectl.RESTClient, error), schema validation.Schema) (resources []Resource) {

	if len(selector) == 0 { // handling filename & resource id
		mapping, namespace, name := ResourceFromArgsOrFile(cmd, args, filename, typer, mapper, schema)
		client, err := clientBuilder(cmd, mapping)
		checkErr(err)
		resources = append(resources, NewResourceInfo(client, mapping, namespace, name))
		return
	}
	labelSelector, err := labels.ParseSelector(selector)
	checkErr(err)
	for _, a := range args {
		for _, arg := range SplitResourceArgument(a, mapper) {
			resource := kubectl.ExpandResourceShortcut(arg)
			if len(resource) == 0 {
				usageError(cmd, "Unknown resource %s", resource)
			}
			version, kind, err := mapper.VersionAndKindForResource(resource)
			checkErr(err)
			mapping, err := mapper.RESTMapping(version, kind)
			checkErr(err)
			client, err := clientBuilder(cmd, mapping)
			checkErr(err)
			namespace := GetKubeNamespace(cmd)
			resources = append(resources, NewResourceSelector(client, mapping, namespace, labelSelector))
		}
	}
	return
}

// ResourceFromArgsOrFile expects two arguments or a valid file with a given type, and extracts
// the fields necessary to uniquely locate a resource. Displays a usageError if that contract is
// not satisfied, or a generic error if any other problems occur.
func ResourceFromArgsOrFile(cmd *cobra.Command, args []string, filename string, typer runtime.ObjectTyper, mapper meta.RESTMapper, schema validation.Schema) (mapping *meta.RESTMapping, namespace, name string) {
	// If command line args are passed in, use those preferentially.
	if len(args) > 0 && len(args) != 2 {
		usageError(cmd, "If passing in command line parameters, must be resource and name")
	}

	if len(args) == 2 {
		resource := kubectl.ExpandResourceShortcut(args[0])
		namespace = GetKubeNamespace(cmd)
		name = args[1]
		if len(name) == 0 || len(resource) == 0 {
			usageError(cmd, "Must specify filename or command line params")
		}

		version, kind, err := mapper.VersionAndKindForResource(resource)
		if err != nil {
			// The error returned by mapper is "no resource defined", which is a usage error
			usageError(cmd, err.Error())
		}

		mapping, err = mapper.RESTMapping(version, kind)
		checkErr(err)
		return
	}

	if len(filename) == 0 {
		usageError(cmd, "Must specify filename or command line params")
	}

	mapping, namespace, name, _ = ResourceFromFile(filename, typer, mapper, schema)
	if len(name) == 0 {
		checkErr(fmt.Errorf("the resource in the provided file has no name (or ID) defined"))
	}

	return
}

// ResourceFromArgs expects two arguments with a given type, and extracts the fields necessary
// to uniquely locate a resource. Displays a usageError if that contract is not satisfied, or
// a generic error if any other problems occur.
func ResourceFromArgs(cmd *cobra.Command, args []string, mapper meta.RESTMapper) (mapping *meta.RESTMapping, namespace, name string) {
	if len(args) != 2 {
		usageError(cmd, "Must provide resource and name command line params")
	}

	resource := kubectl.ExpandResourceShortcut(args[0])
	namespace = GetKubeNamespace(cmd)
	name = args[1]
	if len(name) == 0 || len(resource) == 0 {
		usageError(cmd, "Must provide resource and name command line params")
	}

	version, kind, err := mapper.VersionAndKindForResource(resource)
	checkErr(err)

	mapping, err = mapper.RESTMapping(version, kind)
	checkErr(err)
	return
}

// ResourceFromArgs expects two arguments with a given type, and extracts the fields necessary
// to uniquely locate a resource. Displays a usageError if that contract is not satisfied, or
// a generic error if any other problems occur.
func ResourceOrTypeFromArgs(cmd *cobra.Command, args []string, mapper meta.RESTMapper) (mapping *meta.RESTMapping, namespace, name string) {
	if len(args) == 0 || len(args) > 2 {
		usageError(cmd, "Must provide resource or a resource and name as command line params")
	}

	resource := kubectl.ExpandResourceShortcut(args[0])
	if len(resource) == 0 {
		usageError(cmd, "Must provide resource or a resource and name as command line params")
	}

	namespace = GetKubeNamespace(cmd)
	if len(args) == 2 {
		name = args[1]
		if len(name) == 0 {
			usageError(cmd, "Must provide resource or a resource and name as command line params")
		}
	}

	version, kind, err := mapper.VersionAndKindForResource(resource)
	checkErr(err)

	mapping, err = mapper.RESTMapping(version, kind)
	checkErr(err)

	return
}

// ResourceFromFile retrieves the name and namespace from a valid file. If the file does not
// resolve to a known type an error is returned. The returned mapping can be used to determine
// the correct REST endpoint to modify this resource with.
func ResourceFromFile(filename string, typer runtime.ObjectTyper, mapper meta.RESTMapper, schema validation.Schema) (mapping *meta.RESTMapping, namespace, name string, data []byte) {
	configData, err := ReadConfigData(filename)
	checkErr(err)
	data = configData

	version, kind, err := typer.DataVersionAndKind(data)
	checkErr(err)

	// TODO: allow unversioned objects?
	if len(version) == 0 {
		checkErr(fmt.Errorf("the resource in the provided file has no apiVersion defined"))
	}

	err = schema.ValidateBytes(data)
	checkErr(err)

	mapping, err = mapper.RESTMapping(version, kind)
	checkErr(err)

	obj, err := mapping.Codec.Decode(data)
	checkErr(err)

	meta := mapping.MetadataAccessor
	namespace, err = meta.Namespace(obj)
	checkErr(err)
	name, err = meta.Name(obj)
	checkErr(err)

	return
}

// CompareNamespaceFromFile returns an error if the namespace the user has provided on the CLI
// or via the default namespace file does not match the namespace of an input file. This
// prevents a user from unintentionally updating the wrong namespace.
func CompareNamespaceFromFile(cmd *cobra.Command, namespace string) error {
	defaultNamespace := GetKubeNamespace(cmd)
	if len(namespace) > 0 {
		if defaultNamespace != namespace {
			return fmt.Errorf("the namespace from the provided file %q does not match the namespace %q. You must pass '--namespace=%s' to perform this operation.", namespace, defaultNamespace, namespace)
		}
	}
	return nil
}

func SplitResourceArgument(arg string, mapper meta.RESTMapper) []string {
	set := util.NewStringSet()
	values := strings.Split(arg, ",")
	for _, s := range values {
		switch s {
		case "all":
			set.Insert(mapper.AllResources()...)
		default:
			set.Insert(s)
		}
	}
	return set.List()
}
