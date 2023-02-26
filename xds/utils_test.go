package xds_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/utilitywarehouse/semaphore-xds/xds"
)

func TestParseRetryOn(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		assert.Equal(t, "", xds.ParseRetryOn([]string{}))
		assert.Equal(t, "", xds.ParseRetryOn([]string{""}))
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
			assert.Equal(t, v, xds.ParseRetryOn([]string{v}))
		}
	})

	t.Run("mixed", func(t *testing.T) {
		assert.Equal(t, "some,internal,eggs,cancelled,unavailable", xds.ParseRetryOn([]string{
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
		assert.Equal(t, uint32(1), xds.ParseNumRetries(nil).Value)
	})

	t.Run("valid", func(t *testing.T) {
		var zero uint32 = 0
		assert.Equal(t, uint32(0), xds.ParseNumRetries(&zero).Value)

		var five uint32 = 5
		assert.Equal(t, uint32(5), xds.ParseNumRetries(&five).Value)
	})
}

func TestParseRetryBackOff(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		policy := xds.ParseRetryBackOff("", "")

		assert.Equal(t, 25*time.Millisecond, policy.BaseInterval.AsDuration())
		assert.Equal(t, 250*time.Millisecond, policy.MaxInterval.AsDuration())
	})

	t.Run("max default uses base", func(t *testing.T) {
		policy := xds.ParseRetryBackOff("17ms", "")

		assert.Equal(t, 17*time.Millisecond, policy.BaseInterval.AsDuration())
		assert.Equal(t, 170*time.Millisecond, policy.MaxInterval.AsDuration())
	})

	t.Run("both set", func(t *testing.T) {
		policy := xds.ParseRetryBackOff("9ms", "77ms")

		assert.Equal(t, 9*time.Millisecond, policy.BaseInterval.AsDuration())
		assert.Equal(t, 77*time.Millisecond, policy.MaxInterval.AsDuration())
	})

	t.Run("max not less than base", func(t *testing.T) {
		policy := xds.ParseRetryBackOff("10ms", "7ms")

		assert.Equal(t, 10*time.Millisecond, policy.BaseInterval.AsDuration())
		assert.Equal(t, 10*time.Millisecond, policy.MaxInterval.AsDuration())
	})
}
