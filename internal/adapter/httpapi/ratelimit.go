package httpapi

import "time"

// RateLimiter decides whether one more event for key is allowed now. The in-memory
// implementation is per instance; a shared (e.g. Redis) one can replace it without touching handlers.
type RateLimiter interface {
	Allow(key string) bool
}

// Limits groups the limiters the API applies. Every field must be set.
type Limits struct {
	Register RateLimiter // per IP
	Login    RateLimiter // per IP + email
	LoginIP  RateLimiter // per IP, looser because a school NAT shares one address
	Refresh  RateLimiter // per IP
	Forgot   RateLimiter // per email (forgot and reset)
	ForgotIP RateLimiter // per IP (forgot and reset)

	DeleteAccount RateLimiter // per user id
}

// DefaultLimits returns the production budgets, building each limiter with newLimiter
// (burst events at once, refilling one per interval).
func DefaultLimits(newLimiter func(interval time.Duration, burst int) RateLimiter) Limits {
	return Limits{
		Register: newLimiter(time.Minute/10, 10),
		Login:    newLimiter(time.Minute/5, 5),
		LoginIP:  newLimiter(time.Second, 60),
		Refresh:  newLimiter(time.Minute/30, 30),
		Forgot:   newLimiter(time.Hour/3, 3),
		ForgotIP: newLimiter(time.Minute/10, 10),

		DeleteAccount: newLimiter(time.Minute/5, 5),
	}
}
