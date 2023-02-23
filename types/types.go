package types

// PolicyStrategy determine the strategy to use when assigning policy to
// endpoints
type PolicyStrategy string

const (
	// NoPolicyStrategy means that all endpoints get equally the highest
	// prioritiy
	NoPolicyStrategy PolicyStrategy = "none"
	// LocalFirstPolicyStrategy will give higher priority to local endpoints
	// if they exist
	LocalFirstPolicyStrategy PolicyStrategy = "local-first"
)
