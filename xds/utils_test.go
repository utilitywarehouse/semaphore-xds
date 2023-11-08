package xds

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseRetryOn(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		assert.Equal(t, "", ParseRetryOn([]string{}))
		assert.Equal(t, "", ParseRetryOn([]string{""}))
	})

	t.Run("single", func(t *testing.T) {
		valid := []string{
			"cancelled",
			"deadline-exceeded",
			"internal",
			"resource-exhausted",
			"unavailable",
		}

		for _, v := range valid {
			assert.Equal(t, v, ParseRetryOn([]string{v}))
		}
	})

	t.Run("mixed", func(t *testing.T) {
		assert.Equal(t, "some,internal,eggs,cancelled,unavailable", ParseRetryOn([]string{
			"some",
			"internal",
			"  eggs",
			"cancelled",
			" unavailable",
		}))
	})
}

func TestParseNumRetries(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		assert.Equal(t, uint32(1), ParseNumRetries(nil).Value)
	})

	t.Run("valid", func(t *testing.T) {
		var zero uint32 = 0
		assert.Equal(t, uint32(0), ParseNumRetries(&zero).Value)

		var five uint32 = 5
		assert.Equal(t, uint32(5), ParseNumRetries(&five).Value)
	})
}

func TestParseRetryBackOff(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		policy := ParseRetryBackOff("", "")

		assert.Equal(t, 25*time.Millisecond, policy.BaseInterval.AsDuration())
		assert.Equal(t, 250*time.Millisecond, policy.MaxInterval.AsDuration())
	})

	t.Run("max default uses base", func(t *testing.T) {
		policy := ParseRetryBackOff("17ms", "")

		assert.Equal(t, 17*time.Millisecond, policy.BaseInterval.AsDuration())
		assert.Equal(t, 170*time.Millisecond, policy.MaxInterval.AsDuration())
	})

	t.Run("both set", func(t *testing.T) {
		policy := ParseRetryBackOff("9ms", "77ms")

		assert.Equal(t, 9*time.Millisecond, policy.BaseInterval.AsDuration())
		assert.Equal(t, 77*time.Millisecond, policy.MaxInterval.AsDuration())
	})

	t.Run("max not less than base", func(t *testing.T) {
		policy := ParseRetryBackOff("10ms", "7ms")

		assert.Equal(t, 10*time.Millisecond, policy.BaseInterval.AsDuration())
		assert.Equal(t, 10*time.Millisecond, policy.MaxInterval.AsDuration())
	})
}

func TestResourcesMatch(t *testing.T) {
	// empty lists
	a := []string{}
	b := []string{}
	assert.Equal(t, true, resourcesMatch(a, b))
	// length mismatch
	b = []string{"1"}
	c := []string{"1", "2"}
	assert.Equal(t, false, resourcesMatch(a, b))
	assert.Equal(t, false, resourcesMatch(a, c))
	// different sets
	a = []string{"1", "2"}
	b = []string{"2", "3"}
	assert.Equal(t, false, resourcesMatch(a, b))
	// same sets
	b = []string{"1", "2"}
	assert.Equal(t, true, resourcesMatch(a, b))
	// same sets, mixed values
	b = []string{"2", "1"}
}
