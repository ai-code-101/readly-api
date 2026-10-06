// Command seed creates the starter categories shown in the Readly designs.
// It is idempotent: existing categories (matched by slug) are left untouched.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ai-code-101/readly-api/internal/db"
	"github.com/ai-code-101/readly-api/internal/slug"
)

var categories = []struct{ name, description string }{
	{"Fiction", "Explore timeless stories, character-driven novels, and imaginative worlds that stay with you long after the final page."},
	{"Non-Fiction", "Dive into essays, history, science, and memoirs that illuminate the world through thoughtful research and clear storytelling."},
	{"Mystery", "Uncover suspenseful plots, atmospheric settings, and clever twists that keep you guessing until the very end."},
	{"Romance", "Sweeping love stories, slow-burn connections, and heartfelt journeys of the heart."},
	{"Sci-Fi", "Visionary futures, distant worlds, and big ideas about technology and humanity."},
	{"Fantasy", "Magic, myth, and epic quests across richly imagined realms."},
	{"Thriller", "High-stakes, page-turning suspense that keeps your pulse racing."},
	{"Horror", "Chilling tales of dread, the uncanny, and things that go bump in the night."},
	{"Drama", "Powerful stories of conflict, family, and the human condition."},
	{"Biography", "The remarkable lives of people who shaped history, culture, and ideas."},
	{"History", "Vivid accounts of the people, places, and events that made our world."},
	{"Poetry", "Verse to savour, from classical masters to contemporary voices."},
	{"Philosophy", "Timeless questions about meaning, ethics, and how to live well."},
	{"Travel", "Journeys to faraway places and the stories found along the way."},
	{"Self-Help", "Practical wisdom for growth, habits, and wellbeing."},
	{"Young Adult", "Coming-of-age adventures, first loves, and finding your place in the world."},
}

func main() {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		url = "postgres://postgres:postgres@localhost:5432/readly?sslmode=disable"
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, url)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer pool.Close()
	if err := db.Migrate(ctx, pool); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	created := 0
	for i, c := range categories {
		tag, err := pool.Exec(ctx, `
			INSERT INTO categories (name, slug, description, sort_order)
			VALUES ($1, $2, $3, $4) ON CONFLICT (slug) DO NOTHING`,
			c.name, slug.Make(c.name), c.description, (i+1)*10)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		created += int(tag.RowsAffected())
	}
	fmt.Printf("seed complete: %d new categories (%d total in seed list)\n", created, len(categories))
}
