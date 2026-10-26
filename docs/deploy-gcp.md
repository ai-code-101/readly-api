# Deploying the Readly API on Google Cloud

The API runs on **Cloud Run** and its data lives in **Cloud SQL for PostgreSQL**. Books, categories,
users, subscriptions, reading progress, **EPUB files and cover images** are all in the database
today, so copying the database copies everything. Secrets are kept in **Secret Manager**.

Run these commands in WSL from the `readly-api` folder. Lines starting with `#` are notes.

## 0. One-time setup on your computer

```bash
# Install the Google Cloud CLI: https://cloud.google.com/sdk/docs/install (Debian/Ubuntu section)
gcloud init                      # sign in and choose (or create) your project
```

Billing must be enabled on the project (Console → Billing).

## 1. Settings used by every command below

Paste this into **each new terminal** you use for these steps:

```bash
export PROJECT_ID=your-project-id          # from: gcloud projects list
export REGION=africa-south1                # Johannesburg, closest to Kenya
export INSTANCE=readly-db
export BUCKET=$PROJECT_ID-readly-transfer  # temporary bucket for the database copy
gcloud config set project $PROJECT_ID
```

Turn on the services Readly uses:

```bash
gcloud services enable run.googleapis.com sqladmin.googleapis.com \
  secretmanager.googleapis.com cloudbuild.googleapis.com artifactregistry.googleapis.com
```

## 2. Create the database (takes ~10 minutes)

```bash
gcloud sql instances create $INSTANCE \
  --database-version=POSTGRES_16 --edition=ENTERPRISE --tier=db-f1-micro \
  --region=$REGION --storage-size=10GB --storage-auto-increase

gcloud sql databases create readly --instance=$INSTANCE

export DB_PASS=$(openssl rand -hex 16)
gcloud sql users create readly --instance=$INSTANCE --password="$DB_PASS"
echo "$DB_PASS"                  # keep this somewhere safe
```

`db-f1-micro` is the smallest, cheapest machine; it can be upgraded later without losing data.

## 3. Copy your local database (including EPUBs and covers)

Your local Postgres must be running (`make db-up`). Make a copy of it:

```bash
docker exec readly-api-db-1 pg_dump -U postgres -d readly \
  --format=plain --no-owner --no-acl > readly.sql
sed -i '/^\\\(un\)\?restrict /d' readly.sql     # remove psql-only lines Cloud SQL doesn't need
ls -lh readly.sql
```

Upload it and import it into Cloud SQL:

```bash
gcloud storage buckets create gs://$BUCKET --location=$REGION
gcloud storage cp readly.sql gs://$BUCKET/readly.sql

# Allow Cloud SQL to read the file
SQL_SA=$(gcloud sql instances describe $INSTANCE --format='value(serviceAccountEmailAddress)')
gcloud storage buckets add-iam-policy-binding gs://$BUCKET \
  --member=serviceAccount:$SQL_SA --role=roles/storage.objectViewer

gcloud sql import sql $INSTANCE gs://$BUCKET/readly.sql --database=readly --user=readly
```

Once the API is working (step 6), delete the copy — it contains your users' phone numbers:

```bash
gcloud storage rm -r gs://$BUCKET
rm readly.sql
```

## 4. Store the secrets

```bash
printf '%s' "postgres://readly:$DB_PASS@/readly?host=/cloudsql/$PROJECT_ID:$REGION:$INSTANCE" \
  | gcloud secrets create readly-database-url --data-file=-

# Use the same ADMIN_TOKEN as in readly-admin's .env.local
printf '%s' 'PASTE-YOUR-ADMIN-TOKEN' | gcloud secrets create readly-admin-token --data-file=-
```

Let Cloud Run read the secrets and connect to Cloud SQL:

```bash
PROJECT_NUMBER=$(gcloud projects describe $PROJECT_ID --format='value(projectNumber)')
RUN_SA=$PROJECT_NUMBER-compute@developer.gserviceaccount.com
for role in roles/secretmanager.secretAccessor roles/cloudsql.client; do
  gcloud projects add-iam-policy-binding $PROJECT_ID --member=serviceAccount:$RUN_SA --role=$role
done
```

## 5. Deploy the API

```bash
gcloud run deploy readly-api --source . --region=$REGION --allow-unauthenticated \
  --add-cloudsql-instances=$PROJECT_ID:$REGION:$INSTANCE \
  --set-secrets=DATABASE_URL=readly-database-url:latest,ADMIN_TOKEN=readly-admin-token:latest \
  --set-env-vars='^@^COOKIE_SECURE=true@ALLOWED_ORIGINS=https://readly-wuts.vercel.app,http://localhost:3000,http://localhost:3001' \
  --memory=1Gi
```

The first deploy asks to create an Artifact Registry repository — answer **Y**. It builds the
`Dockerfile` in Google Cloud (nothing needs to be built locally) and prints the service URL, e.g.
`https://readly-api-xxxxxxxx.africa-south1.run.app`. The API applies any pending database migrations
when it starts.

To deploy a new version later, run the same command again.

## 6. Check it

```bash
URL=$(gcloud run services describe readly-api --region=$REGION --format='value(status.url)')
curl $URL/healthz                         # {"status":"ok"}
curl "$URL/api/v1/books" | head -c 300    # your books
```

Logs: `gcloud run services logs read readly-api --region=$REGION` or Console → Cloud Run → readly-api → Logs.

## 7. Point the apps at Cloud Run

- **Readly platform (Vercel):** Settings → Environment Variables → `READLY_API_URL` = the Cloud Run URL,
  then redeploy.
- **readly-admin:** set its API URL in `.env.local` to the Cloud Run URL and restart it.
- ngrok is no longer needed.

## Limits to know

- **Uploads are limited to 32 MB per request on Cloud Run**, so EPUBs larger than that can't be
  uploaded through the admin yet. Moving EPUBs and covers to Cloud Storage removes this limit.
- Cost is mainly the Cloud SQL instance (roughly USD 10–15/month for `db-f1-micro`); Cloud Run is
  usually within the free tier at low traffic.
