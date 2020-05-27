package cmdg

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	maxContacts      = 10000
	contactBatchSize = 50
)

var (
	// Valid RFC5322 comment field. Actually this is a bit
	// restrictive since some other chars are allowed per section
	// 3.2.3. But this is playing it safe for now.
	rfc5322commentRE = regexp.MustCompile(`^[A-Za-z0-9]+$`)
)

func (c *CmdG) Contacts() []string {
	c.m.RLock()
	defer c.m.RUnlock()
	return append([]string{"me"}, c.contacts...)
}

func (c *CmdG) LoadContacts(ctx context.Context) error {
	co, err := c.GetContacts(ctx)
	if err != nil {
		return err
	}
	c.m.Lock()
	defer c.m.Unlock()
	c.contacts = co
	return nil
}

func quoteNameIfNeeded(s string) string {
	if rfc5322commentRE.MatchString(s) {
		return s
	}
	return fmt.Sprintf("%q", s)
}

// GetContacts gets all contact's email addresses in "Name Name <email@example.com>" format.
func (c *CmdG) GetContacts(ctx context.Context) ([]string, error) {
	// List contacts.
	r, err := c.people.ContactGroups.Get("contactGroups/all").Context(ctx).MaxMembers(maxContacts).Do()
	if err != nil {
		return nil, err
	}
	log.Infof("Retrieved %d of %d contacts", len(r.MemberResourceNames), r.MemberCount)

	// Get contact names/email addresses.
	var wg sync.WaitGroup
	pchan := make(chan string)
	batches := len(r.MemberResourceNames)/contactBatchSize + 1
	perr := make(chan error, batches)
	for n := 0; ; n++ {
		start := n * contactBatchSize
		end := (n + 1) * contactBatchSize
		if start >= len(r.MemberResourceNames) {
			break
		}
		if end > len(r.MemberResourceNames) {
			end = len(r.MemberResourceNames)
		}
		batch := r.MemberResourceNames[start:end]
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				p, err := c.people.People.GetBatchGet().Context(ctx).ResourceNames(batch...).PersonFields("names,emailAddresses").Do()
				if err != nil {
					log.Warningf("Error loading contacts: %v", err)
					if strings.Contains(err.Error(), "quota") {
						time.Sleep(time.Second)
						continue
					}
					perr <- err
					return
				}
				for _, r := range p.Responses {
					// Use name first listed.
					name := ""
					if len(r.Person.Names) > 0 {
						name = r.Person.Names[0].DisplayName
					}
					for _, e := range r.Person.EmailAddresses {
						if strings.Contains(e.Value, " ") {
							// Name already there.
							log.Warningf("Contact email address contains a space: %q", e.Value)
							pchan <- e.Value
						} else {
							if len(name) > 0 {
								pchan <- fmt.Sprintf(`%s <%s>`, quoteNameIfNeeded(name), e.Value)
							} else {
								pchan <- e.Value
							}
						}
					}
				}
				return
			}
		}()
	}
	go func() {
		wg.Wait()
		close(pchan)
		close(perr)
	}()
	var ret []string
	for s := range pchan {
		ret = append(ret, s)
	}
	for e := range perr {
		return nil, e
	}
	sort.Slice(ret, func(i, j int) bool {
		return strings.TrimLeft(ret[i], `"`) < strings.TrimLeft(ret[j], `"`)
	})
	return ret, nil
}
