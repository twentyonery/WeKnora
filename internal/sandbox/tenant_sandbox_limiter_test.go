package sandbox

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTenantSandboxLimiterUnlimitedWhenLimitNonPositive(t *testing.T) {
	limiter := NewTenantSandboxLimiter()
	for range 5 {
		require.NoError(t, limiter.Acquire(1, "cfg", 0))
	}
	assert.Equal(t, 0, limiter.Live(1, "cfg"), "unlimited mode does not count")
}

func TestTenantSandboxLimiterBlocksAtCapAndIsolatesSlots(t *testing.T) {
	limiter := NewTenantSandboxLimiter()

	require.NoError(t, limiter.Acquire(1, "cfg-a", 2))
	require.NoError(t, limiter.Acquire(1, "cfg-a", 2))
	assert.Equal(t, 2, limiter.Live(1, "cfg-a"))

	err := limiter.Acquire(1, "cfg-a", 2)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTenantSandboxLimitExceeded))

	// A different tenant and a different config do not share slots.
	require.NoError(t, limiter.Acquire(2, "cfg-a", 2))
	require.NoError(t, limiter.Acquire(1, "cfg-b", 2))
	assert.Equal(t, 2, limiter.Live(1, "cfg-a"))
	assert.Equal(t, 1, limiter.Live(2, "cfg-a"))
	assert.Equal(t, 1, limiter.Live(1, "cfg-b"))

	limiter.Release(1, "cfg-a")
	require.NoError(t, limiter.Acquire(1, "cfg-a", 2))
}

func TestTenantSandboxLimiterReleaseClampsAtZero(t *testing.T) {
	limiter := NewTenantSandboxLimiter()

	// Adoption-style release: no matching acquire must not go negative.
	limiter.Release(1, "cfg-a")
	assert.Equal(t, 0, limiter.Live(1, "cfg-a"))

	// After clamping, the full cap must still be grantable.
	for range 3 {
		require.NoError(t, limiter.Acquire(1, "cfg-a", 3))
	}
	assert.Equal(t, 3, limiter.Live(1, "cfg-a"))
	assert.ErrorIs(t, limiter.Acquire(1, "cfg-a", 3), ErrTenantSandboxLimitExceeded)
}
