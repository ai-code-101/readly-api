-- Phone numbers now come from Safaricom (subscription notifications / header
-- enrichment) instead of SMS one-time codes, so the OTP table is no longer used.
DROP TABLE IF EXISTS otp_requests;
