// Package fakes provides in-memory implementations of the domain interfaces
// for tests. Each method calls its function field when set and otherwise
// returns a harmless default. Calls are recorded and safe for concurrent use.
package fakes

import (
	"context"
	"sync"

	"github.com/cruxstack/github-ops-app/internal/domain"
)

// calls records method calls.
type calls struct {
	mu    sync.Mutex
	names []string
}

func (c *calls) record(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.names = append(c.names, name)
}

// Count returns how many times the named method was called.
func (c *calls) Count(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, got := range c.names {
		if got == name {
			n++
		}
	}
	return n
}

// GitHub is a fake domain.GitHubClient.
type GitHub struct {
	calls

	CheckPRComplianceFn  func(ctx context.Context, owner, repo string, prNumber int) (*domain.PRComplianceResult, error)
	ResolveTeamFn        func(ctx context.Context, name, privacy string, create bool) (string, error)
	SyncTeamMembersFn    func(ctx context.Context, teamSlug string, desired []string, threshold float64) (*domain.TeamSyncResult, error)
	GetTeamMembersFn     func(ctx context.Context, teamSlug string) ([]string, error)
	ListOrgMembersFn     func(ctx context.Context) ([]string, error)
	GetAppSlugFn         func(ctx context.Context) (string, error)
	ListSecurityAlertsFn func(ctx context.Context, minAgeDays int, minSeverity string) (*domain.SecurityAlertsReport, error)
}

var _ domain.GitHubClient = (*GitHub)(nil)

func (f *GitHub) CheckPRCompliance(ctx context.Context, owner, repo string, prNumber int) (*domain.PRComplianceResult, error) {
	f.record("CheckPRCompliance")
	if f.CheckPRComplianceFn != nil {
		return f.CheckPRComplianceFn(ctx, owner, repo, prNumber)
	}
	return &domain.PRComplianceResult{}, nil
}

func (f *GitHub) ResolveTeam(ctx context.Context, name, privacy string, create bool) (string, error) {
	f.record("ResolveTeam")
	if f.ResolveTeamFn != nil {
		return f.ResolveTeamFn(ctx, name, privacy, create)
	}
	return name, nil
}

func (f *GitHub) SyncTeamMembers(ctx context.Context, teamSlug string, desired []string, threshold float64) (*domain.TeamSyncResult, error) {
	f.record("SyncTeamMembers")
	if f.SyncTeamMembersFn != nil {
		return f.SyncTeamMembersFn(ctx, teamSlug, desired, threshold)
	}
	return &domain.TeamSyncResult{TeamName: teamSlug, MembersAdded: desired, Members: desired}, nil
}

func (f *GitHub) GetTeamMembers(ctx context.Context, teamSlug string) ([]string, error) {
	f.record("GetTeamMembers")
	if f.GetTeamMembersFn != nil {
		return f.GetTeamMembersFn(ctx, teamSlug)
	}
	return nil, nil
}

func (f *GitHub) ListOrgMembers(ctx context.Context) ([]string, error) {
	f.record("ListOrgMembers")
	if f.ListOrgMembersFn != nil {
		return f.ListOrgMembersFn(ctx)
	}
	return nil, nil
}

func (f *GitHub) GetAppSlug(ctx context.Context) (string, error) {
	f.record("GetAppSlug")
	if f.GetAppSlugFn != nil {
		return f.GetAppSlugFn(ctx)
	}
	return "ops-app", nil
}

func (f *GitHub) ListSecurityAlerts(ctx context.Context, minAgeDays int, minSeverity string) (*domain.SecurityAlertsReport, error) {
	f.record("ListSecurityAlerts")
	if f.ListSecurityAlertsFn != nil {
		return f.ListSecurityAlertsFn(ctx, minAgeDays, minSeverity)
	}
	return &domain.SecurityAlertsReport{
		MinAgeDays:   minAgeDays,
		MinSeverity:  minSeverity,
		AlertsByRepo: map[string][]domain.SecurityAlert{},
	}, nil
}

// Okta is a fake domain.OktaClient.
type Okta struct {
	calls

	GetGroupsByPatternFn func(ctx context.Context, pattern string) ([]*domain.GroupInfo, error)
	GetGroupInfoFn       func(ctx context.Context, name string) (*domain.GroupInfo, error)
}

var _ domain.OktaClient = (*Okta)(nil)

func (f *Okta) GetGroupsByPattern(ctx context.Context, pattern string) ([]*domain.GroupInfo, error) {
	f.record("GetGroupsByPattern")
	if f.GetGroupsByPatternFn != nil {
		return f.GetGroupsByPatternFn(ctx, pattern)
	}
	return nil, nil
}

func (f *Okta) GetGroupInfo(ctx context.Context, name string) (*domain.GroupInfo, error) {
	f.record("GetGroupInfo")
	if f.GetGroupInfoFn != nil {
		return f.GetGroupInfoFn(ctx, name)
	}
	return &domain.GroupInfo{ID: "id-" + name, Name: name}, nil
}

// Notifier is a fake domain.Notifier that keeps what it was asked to send.
type Notifier struct {
	calls
	mu sync.Mutex

	Err error

	PRBypasses     []*domain.PRComplianceResult
	OktaSyncs      [][]*domain.SyncReport
	OrphanedUsers  []*domain.OrphanedUsersReport
	SecurityAlerts []*domain.SecurityAlertsReport
}

var _ domain.Notifier = (*Notifier)(nil)

func (f *Notifier) NotifyPRBypass(_ context.Context, result *domain.PRComplianceResult, _ string) error {
	f.record("NotifyPRBypass")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.PRBypasses = append(f.PRBypasses, result)
	return f.Err
}

func (f *Notifier) NotifyOktaSync(_ context.Context, reports []*domain.SyncReport, _ string) error {
	f.record("NotifyOktaSync")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.OktaSyncs = append(f.OktaSyncs, reports)
	return f.Err
}

func (f *Notifier) NotifyOrphanedUsers(_ context.Context, report *domain.OrphanedUsersReport) error {
	f.record("NotifyOrphanedUsers")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.OrphanedUsers = append(f.OrphanedUsers, report)
	return f.Err
}

func (f *Notifier) NotifySecurityAlerts(_ context.Context, report *domain.SecurityAlertsReport, _ string) error {
	f.record("NotifySecurityAlerts")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.SecurityAlerts = append(f.SecurityAlerts, report)
	return f.Err
}
