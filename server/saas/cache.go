package saas

import (
	"context"
	"sync"
	"time"
)

type (
	// accessCache keeps short-lived membership/company lookups so the API
	// gate does not hit the database on every request. Entries are
	// invalidated explicitly on every state change made by this node and
	// expire quickly so changes made by other nodes propagate.
	accessCache struct {
		ttl time.Duration
		mu  sync.RWMutex

		members   map[uint64]memberEntry
		companies map[uint64]companyEntry
	}

	memberEntry struct {
		m   *Member
		err error
		exp time.Time
	}

	companyEntry struct {
		c   *Company
		exp time.Time
	}
)

func newAccessCache(ttl time.Duration) *accessCache {
	return &accessCache{
		ttl:       ttl,
		members:   make(map[uint64]memberEntry),
		companies: make(map[uint64]companyEntry),
	}
}

func (c *accessCache) lookup(ctx context.Context, repo *Repo, userID uint64) (*Member, *Company, error) {
	now := time.Now()

	c.mu.RLock()
	me, okM := c.members[userID]
	c.mu.RUnlock()

	if !okM || now.After(me.exp) {
		m, err := repo.MemberByUser(ctx, userID)
		if err != nil && err != ErrNotFound {
			return nil, nil, err
		}

		me = memberEntry{m: m, err: err, exp: now.Add(c.ttl)}
		c.mu.Lock()
		c.members[userID] = me
		c.mu.Unlock()
	}

	if me.err != nil {
		return nil, nil, me.err
	}

	c.mu.RLock()
	ce, okC := c.companies[me.m.CompanyID]
	c.mu.RUnlock()

	if !okC || now.After(ce.exp) {
		co, err := repo.CompanyByID(ctx, me.m.CompanyID)
		if err != nil {
			return nil, nil, err
		}

		ce = companyEntry{c: co, exp: now.Add(c.ttl)}
		c.mu.Lock()
		c.companies[me.m.CompanyID] = ce
		c.mu.Unlock()
	}

	return me.m, ce.c, nil
}

func (c *accessCache) invalidateUser(userID uint64) {
	c.mu.Lock()
	delete(c.members, userID)
	c.mu.Unlock()
}

func (c *accessCache) invalidateCompany(companyID uint64) {
	c.mu.Lock()
	delete(c.companies, companyID)
	c.mu.Unlock()
}
