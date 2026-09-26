package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/popinjayjohn/dine-and-dash-semiplane/internal/domain"
)

// campaignColumns is the column list every campaign query selects, in the order
// scanCampaign expects. One constant so a new column cannot be added to the
// schema and forgotten by half the queries.
const campaignColumns = `id, slug, name, system, vault_dir, created_at, updated_at`

// CreateCampaign stores a new campaign and returns it as stored.
//
// A campaign id the caller left empty is minted, and both timestamps are
// stamped. The slug is the campaign's permanent identity: it is a UNIQUE column
// and there is no method that changes it, because every shared link and every
// directory name already contains it.
func (s *Store) CreateCampaign(ctx context.Context, c domain.Campaign) (domain.Campaign, error) {
	if c.ID == "" {
		c.ID = s.mint()
	}
	now := s.now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	if c.System == "" {
		c.System = domain.DefaultSystem
	}

	if err := c.Validate(); err != nil {
		return domain.Campaign{}, err
	}

	const query = `INSERT INTO campaigns (id, slug, name, system, vault_dir, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`

	_, err := s.write.ExecContext(ctx, query,
		c.ID, c.Slug.String(), c.Name, c.System, c.VaultDir,
		c.CreatedAt.UTC().Format(timeLayout), c.UpdatedAt.UTC().Format(timeLayout))
	if err != nil {
		return domain.Campaign{}, writeError(fmt.Sprintf("creating campaign %q", c.Slug), err)
	}

	return c, nil
}

// GetCampaign returns the campaign with the given id.
func (s *Store) GetCampaign(ctx context.Context, id string) (domain.Campaign, error) {
	const query = `SELECT ` + campaignColumns + ` FROM campaigns WHERE id = ?`

	c, err := scanCampaign(s.read.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Campaign{}, notFound("campaign", id)
	}
	if err != nil {
		return domain.Campaign{}, fmt.Errorf("reading campaign %s: %w", id, err)
	}
	return c, nil
}

// CampaignBySlug returns the campaign with the given slug, which is how a
// request for /c/<slug> finds it.
func (s *Store) CampaignBySlug(ctx context.Context, slug domain.Slug) (domain.Campaign, error) {
	const query = `SELECT ` + campaignColumns + ` FROM campaigns WHERE slug = ?`

	c, err := scanCampaign(s.read.QueryRowContext(ctx, query, slug.String()))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Campaign{}, notFound("campaign", slug)
	}
	if err != nil {
		return domain.Campaign{}, fmt.Errorf("reading campaign %q: %w", slug, err)
	}
	return c, nil
}

// ListCampaigns returns every campaign, ordered by slug. The order is part of
// the contract rather than a convenience: a list whose order changes between
// runs cannot be asserted on.
func (s *Store) ListCampaigns(ctx context.Context) ([]domain.Campaign, error) {
	const query = `SELECT ` + campaignColumns + ` FROM campaigns ORDER BY slug`

	rows, err := s.read.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("listing campaigns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	campaigns := []domain.Campaign{}
	for rows.Next() {
		c, err := scanCampaign(rows)
		if err != nil {
			return nil, fmt.Errorf("listing campaigns: %w", err)
		}
		campaigns = append(campaigns, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("listing campaigns: %w", err)
	}

	return campaigns, nil
}

// UpdateCampaign changes the mutable parts of a campaign: its name, its
// ruleset and where its vault lives. It cannot change the slug or the id, and
// the query does not mention either.
//
// The id is not in the WHERE clause either. A campaign is addressed by its id
// because its slug is immutable, so there is nothing to confuse the two.
func (s *Store) UpdateCampaign(ctx context.Context, c domain.Campaign) (domain.Campaign, error) {
	if c.System == "" {
		c.System = domain.DefaultSystem
	}
	if err := c.Validate(); err != nil {
		return domain.Campaign{}, err
	}

	c.UpdatedAt = s.now()

	const query = `UPDATE campaigns SET name = ?, system = ?, vault_dir = ?, updated_at = ?
		WHERE id = ?`

	result, err := s.write.ExecContext(ctx, query,
		c.Name, c.System, c.VaultDir, c.UpdatedAt.Format(timeLayout), c.ID)
	if err != nil {
		return domain.Campaign{}, writeError(fmt.Sprintf("updating campaign %s", c.ID), err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return domain.Campaign{}, fmt.Errorf("updating campaign %s: %w", c.ID, err)
	}
	if affected == 0 {
		return domain.Campaign{}, notFound("campaign", c.ID)
	}

	return c, nil
}

func scanCampaign(row rowScanner) (domain.Campaign, error) {
	var (
		c                              domain.Campaign
		slug, system, created, updated string
	)

	err := row.Scan(&c.ID, &slug, &c.Name, &system, &c.VaultDir, &created, &updated)
	if err != nil {
		return domain.Campaign{}, err
	}

	c.Slug = domain.Slug(slug)
	c.System = system
	if c.CreatedAt, err = requiredTime("campaigns.created_at", created); err != nil {
		return domain.Campaign{}, err
	}
	if c.UpdatedAt, err = requiredTime("campaigns.updated_at", updated); err != nil {
		return domain.Campaign{}, err
	}

	return c, nil
}
