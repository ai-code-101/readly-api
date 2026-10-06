-- Binary assets (EPUBs, covers, thumbnails, category images) live in their own
-- table so that listing queries on books/categories never touch the bytea data.
CREATE TABLE files (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        text        NOT NULL CHECK (kind IN ('epub', 'cover', 'cover_thumb', 'category_image')),
    mime_type   text        NOT NULL,
    size_bytes  bigint      NOT NULL,
    sha256      text        NOT NULL,
    data        bytea       NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE categories (
    id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name           text        NOT NULL,
    slug           text        NOT NULL UNIQUE,
    description    text        NOT NULL DEFAULT '',
    sort_order     int         NOT NULL DEFAULT 0,
    image_file_id  uuid        REFERENCES files(id) ON DELETE SET NULL,
    created_at     timestamptz NOT NULL DEFAULT now(),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE books (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    title                text          NOT NULL,
    slug                 text          NOT NULL UNIQUE,
    author               text          NOT NULL DEFAULT '',
    synopsis             text          NOT NULL DEFAULT '',
    language             text          NOT NULL DEFAULT 'en',
    genre_tag            text          NOT NULL DEFAULT '',
    category_id          uuid          REFERENCES categories(id) ON DELETE SET NULL,
    rating               numeric(2,1)  NOT NULL DEFAULT 0 CHECK (rating >= 0 AND rating <= 5),
    page_count           int           NOT NULL DEFAULT 0 CHECK (page_count >= 0),
    word_count           int           NOT NULL DEFAULT 0 CHECK (word_count >= 0),
    is_free              boolean       NOT NULL DEFAULT false,
    is_trending          boolean       NOT NULL DEFAULT false,
    is_staff_pick        boolean       NOT NULL DEFAULT false,
    is_book_of_the_day   boolean       NOT NULL DEFAULT false,
    status               text          NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'published')),
    open_count           bigint        NOT NULL DEFAULT 0,
    epub_file_id         uuid          REFERENCES files(id) ON DELETE SET NULL,
    cover_file_id        uuid          REFERENCES files(id) ON DELETE SET NULL,
    cover_thumb_file_id  uuid          REFERENCES files(id) ON DELETE SET NULL,
    published_at         timestamptz,
    created_at           timestamptz   NOT NULL DEFAULT now(),
    updated_at           timestamptz   NOT NULL DEFAULT now()
);

CREATE INDEX books_category_idx ON books (category_id) WHERE status = 'published';
CREATE INDEX books_published_at_idx ON books (published_at DESC) WHERE status = 'published';

-- Only one book can be "Book of the Day" at a time.
CREATE UNIQUE INDEX books_one_book_of_the_day ON books (is_book_of_the_day) WHERE is_book_of_the_day;
