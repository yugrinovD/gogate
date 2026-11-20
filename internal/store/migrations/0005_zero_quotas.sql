-- Zero limits were ambiguous: Lua treated them as "unlimited". Normalize old
-- rows to NULL, which is the documented way to remove a limit.
UPDATE quotas SET rpm = NULL WHERE rpm = 0;
UPDATE quotas SET tpm = NULL WHERE tpm = 0;
UPDATE quotas SET daily_requests = NULL WHERE daily_requests = 0;
UPDATE quotas SET daily_tokens = NULL WHERE daily_tokens = 0;
UPDATE quotas SET monthly_cost_usd = NULL WHERE monthly_cost_usd = 0;
