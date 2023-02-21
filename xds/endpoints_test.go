package xds

import (
	"testing"

	"github.com/stretchr/testify/assert"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestXdsEndpointStore_Empty(t *testing.T) {
	endpointStore := NewXdsEnpointStore()
	assert.Equal(t, 0, len(endpointStore.All()))
}

func TestXdsEndpointStore_AddNew(t *testing.T) {
	endpointStore := NewXdsEnpointStore()
	endpointSlice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abc",
			Namespace: "bar",
		}}
	endpointStore.Add("foo", "bar", endpointSlice, uint32(0))
	assert.Equal(t, 1, len(endpointStore.All()))
	e := endpointStore.Get("foo", "bar")
	assert.Equal(t, 1, len(e.endpointSlices))
	assert.Equal(t, "foo-abc", e.endpointSlices[0].endpointSlice.Name)
	assert.Equal(t, uint32(0), e.endpointSlices[0].priority)
}

func TestXdsEndpointStore_AddManyEndpointsSameService(t *testing.T) {
	endpointStore := NewXdsEnpointStore()
	endpointSlice := &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-abc",
			Namespace: "bar",
		}}
	endpointStore.Add("foo", "bar", endpointSlice, uint32(0))
	assert.Equal(t, 1, len(endpointStore.All()))
	e := endpointStore.Get("foo", "bar")
	assert.Equal(t, 1, len(e.endpointSlices))
	assert.Equal(t, "foo-abc", e.endpointSlices[0].endpointSlice.Name)
	assert.Equal(t, uint32(0), e.endpointSlices[0].priority)
	// Add another EndpointSlice for the same Service
	endpointSlice = &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foo-def",
			Namespace: "bar",
		}}
	endpointStore.Add("foo", "bar", endpointSlice, uint32(0))
	assert.Equal(t, 1, len(endpointStore.All()))
	e = endpointStore.Get("foo", "bar")
	assert.Equal(t, 2, len(e.endpointSlices))
	assert.Equal(t, "foo-abc", e.endpointSlices[0].endpointSlice.Name)
	assert.Equal(t, "foo-def", e.endpointSlices[1].endpointSlice.Name)
}
