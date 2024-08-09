// Copied from https://github.com/kubernetes/kubectl/blob/a6de79e3d40575480caa71a8e5efffeea6587251/pkg/cmd/get/table_printer.go

package list

import (
	"errors"
	"io"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	metav1beta1 "k8s.io/apimachinery/pkg/apis/meta/v1beta1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/cli-runtime/pkg/printers"
	"k8s.io/klog/v2"
)

// TablePrinter decodes table objects into typed objects before delegating to another printer.
// Non-table types are simply passed through
type TablePrinter struct {
	Delegate printers.ResourcePrinter
}

func (t *TablePrinter) PrintObj(obj runtime.Object, writer io.Writer) error {
	table, err := decodeIntoTable(obj)
	if err == nil {
		return t.Delegate.PrintObj(table, writer)
	}
	// if we are unable to decode server response into a v1beta1.Table,
	// fallback to client-side printing with whatever info the server returned.
	klog.V(2).Infof("Unable to decode server response into a Table. Falling back to hardcoded types: %v", err)
	return t.Delegate.PrintObj(obj, writer)
}

var recognizedTableVersions = map[schema.GroupVersionKind]bool{
	metav1beta1.SchemeGroupVersion.WithKind("Table"): true,
	metav1.SchemeGroupVersion.WithKind("Table"):      true,
}

// assert the types are identical, since we're decoding both types into a metav1.Table
var _ metav1.Table = metav1beta1.Table{}
var _ metav1beta1.Table = metav1.Table{}

func decodeIntoTable(obj runtime.Object) (runtime.Object, error) {
	event, isEvent := obj.(*metav1.WatchEvent)
	if isEvent {
		obj = event.Object.Object
	}

	if !recognizedTableVersions[obj.GetObjectKind().GroupVersionKind()] {
		return nil, errors.New("attempt to decode non-Table object")
	}

	unstr, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return nil, errors.New("attempt to decode non-Unstructured object")
	}
	table := &metav1.Table{}
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(unstr.Object, table); err != nil {
		return nil, err
	}

	for i := range table.Rows {
		row := &table.Rows[i]
		if row.Object.Raw == nil || row.Object.Object != nil {
			continue
		}
		converted, err := runtime.Decode(unstructured.UnstructuredJSONScheme, row.Object.Raw)
		if err != nil {
			return nil, err
		}
		row.Object.Object = converted
	}

	if isEvent {
		event.Object.Object = table
		return event, nil
	}
	return table, nil
}
