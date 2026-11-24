package gateway

// BreakerCount is the number of circuit breakers the Client holds, one per use
// case that has passed the policy gate. For tests only.
func (c *Client) BreakerCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.breakers)
}
