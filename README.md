# readly-api

Go REST API for **Readly**, a digital library of EPUB books. It stores book metadata, EPUB files, covers
and category images in **PostgreSQL** and serves them to:

- [`readly`](https://github.com/ai-code-101/readly) – the reader web app (Next.js)
- [`readly-admin`](https://github.com/ai-code-101/readly-admin) – the admin app for uploading books (Next.js)

## Quick start

```bash
# 1. Postgres (or bring your own and set DATABASE_URL)
docker compose up -d db

# 2. Config
cp .env.example .env            # set ADMIN_TOKEN

# 3. Create the starter categories, then run the API on :8080
make seed
make run
```

Or run everything in Docker: `docker compose up --build`, then `docker compose run --rm seed`.

Migrations in `internal/db/migrations` are embedded and applied automatically at start-up.

## Configuration

| Variable          | Default                                                        | Notes |
|-------------------|----------------------------------------------------------------|-------|
| `ADDR`            | `:8080`                                                        | Listen address |
| `DATABASE_URL`    | `postgres://postgres:postgres@localhost:5432/readly?sslmode=disable` | |
| `ADMIN_TOKEN`     | — (required)                                                   | Bearer token for `/api/v1/admin/*` |
| `ALLOWED_ORIGINS` | `http://localhost:3000,http://localhost:3001`                  | CORS allow-list |
| `MAX_EPUB_MB`     | `100`                                                          | Upload limit for EPUBs |
| `MAX_IMAGE_MB`    | `10`                                                           | Upload limit for covers / category images |

## Storage

All binary assets live in the `files` table (`bytea`), separate from `books` and `categories`, so list
queries never load file data. Covers get a 400px JPEG thumbnail generated on upload. Asset URLs carry a
content hash (`?v=…`) and are served with long-lived cache headers; EPUBs are served with `ETag` and
HTTP Range support.

## API

All responses are JSON; asset URLs in responses are relative to the API origin.

### Public

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/categories` | All categories with published book counts |
| GET | `/api/v1/categories/{slug}` | One category |
| GET | `/api/v1/categories/{slug}/image` | Category image |
| GET | `/api/v1/books` | Published books. Query: `category`, `q`, `sort` (`popular`\|`new`\|`rating`\|`title`), `trending`, `staffPick`, `free`, `page`, `pageSize` |
| GET | `/api/v1/featured/book-of-the-day` | The current Book of the Day |
| GET | `/api/v1/books/{slug}` | Book detail |
| GET | `/api/v1/books/{slug}/related` | Similar books (same category first) |
| GET | `/api/v1/books/{slug}/cover[?size=thumb]` | Cover image or thumbnail |
| GET | `/api/v1/books/{slug}/epub` | The EPUB file (counts towards "Most Popular") |

### Admin (`Authorization: Bearer $ADMIN_TOKEN`)

| Method | Path | Description |
|---|---|---|
| GET | `/api/v1/admin/stats` | Totals for the dashboard |
| GET/POST | `/api/v1/admin/categories` | List / create (`{name, description, sortOrder}`) |
| PUT/DELETE | `/api/v1/admin/categories/{id}` | Update / delete |
| PUT | `/api/v1/admin/categories/{id}/image` | Multipart `image` |
| POST | `/api/v1/admin/epub/inspect` | Multipart `epub` → extracted title, author, language, description, page estimate, whether a cover exists |
| GET | `/api/v1/admin/books` | All books including drafts (same filters as public, plus `status=draft\|published`) |
| POST | `/api/v1/admin/books` | Multipart: `epub` (required), `cover` (optional – falls back to the EPUB's own cover), `metadata` (JSON, see below – blank fields are filled from the EPUB) |
| GET/PUT/DELETE | `/api/v1/admin/books/{id}` | Read / replace metadata (JSON) / delete with its files |
| PUT | `/api/v1/admin/books/{id}/epub` | Replace the EPUB (multipart `epub`) |
| PUT | `/api/v1/admin/books/{id}/cover` | Replace the cover (multipart `cover`) |
| GET | `/api/v1/admin/books/{id}/cover`, `/epub` | Preview assets, including for drafts |

Book metadata:

```json
{
  "title": "The Glass House", "author": "Elena Vance", "synopsis": "…",
  "language": "en", "genreTag": "Literary Fiction", "categoryId": "<uuid>|null",
  "rating": 4.9, "pageCount": 312,
  "isFree": false, "isTrending": true, "isStaffPick": false, "isBookOfTheDay": false,
  "status": "draft|published"
}
```

## Tests

```bash
go test ./...                                                     # unit tests
TEST_DATABASE_URL=postgres://postgres:postgres@localhost:5432/readly_test?sslmode=disable go test ./...
```

The integration test **drops and recreates the `public` schema** of `TEST_DATABASE_URL` – point it at a
throwaway database.

## Not yet implemented (planned)

- User sign-up / login (email OTP + Google) and admin accounts (replacing `ADMIN_TOKEN`)
- Free plan: 5 free books, then the paywall (hook point: `bookEPUB` in `internal/httpapi/public.go`)
- M-Pesa (Safaricom STK push) subscriptions
- Moving file storage from Postgres to object storage for production
