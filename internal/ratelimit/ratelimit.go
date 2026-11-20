// Package ratelimit implements atomic RPM/TPM and quota checks in Redis.
package ratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Limit describes one scope (tenant or key) to enforce.
type Limit struct {
	ID             string   `json:"id"`
	RPM            *int     `json:"rpm,omitempty"`
	TPM            *int     `json:"tpm,omitempty"`
	DailyRequests  *int64   `json:"daily_requests,omitempty"`
	DailyTokens    *int64   `json:"daily_tokens,omitempty"`
	MonthlyCostUSD *float64 `json:"monthly_cost_usd,omitempty"`
}

// Decision is the result of an admission check.
type Decision struct {
	Allowed    bool
	Reason     string
	Scope      string
	RetryAfter time.Duration
}

// Limiter enforces limits using a single Lua script.
type Limiter struct {
	client *redis.Client
	admit  *redis.Script
	settle *redis.Script
}

// New creates a limiter.
func New(client *redis.Client) *Limiter {
	return &Limiter{
		client: client,
		admit:  redis.NewScript(admitScript),
		settle: redis.NewScript(settleScript),
	}
}

// Admit atomically checks all scopes and reserves tokens/cost.
func (l *Limiter) Admit(ctx context.Context, scopes []Limit, tokens int, cost float64) (Decision, error) {
	if len(scopes) == 0 {
		return Decision{Allowed: true}, nil
	}
	payload, err := json.Marshal(scopes)
	if err != nil {
		return Decision{}, err
	}
	now := time.Now().UTC()
	res, err := l.admit.Run(ctx, l.client, nil,
		now.UnixMilli()*1000, tokens, cost, string(payload), now.Format("20060102"), now.Format("200601")).Slice()
	if err != nil {
		return Decision{}, err
	}
	if len(res) < 4 {
		return Decision{}, fmt.Errorf("unexpected rate limit response")
	}
	allowed, _ := res[0].(int64)
	if allowed == 1 {
		return Decision{Allowed: true}, nil
	}
	wait, _ := res[1].(int64)
	scope, _ := res[2].(string)
	reason, _ := res[3].(string)
	return Decision{Allowed: false, RetryAfter: time.Duration(wait) * time.Second, Scope: scope, Reason: reason}, nil
}

// Settle adjusts reservations after the actual usage is known.
func (l *Limiter) Settle(ctx context.Context, scopes []Limit, tokensDelta int, costDelta float64) error {
	if len(scopes) == 0 || (tokensDelta == 0 && costDelta == 0) {
		return nil
	}
	payload, err := json.Marshal(scopes)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return l.settle.Run(ctx, l.client, nil,
		now.UnixMilli()*1000, tokensDelta, costDelta, string(payload), now.Format("20060102"), now.Format("200601")).Err()
}

// RetryAfterHeader formats a Retry-After value.
func RetryAfterHeader(d time.Duration) string {
	s := int64(d / time.Second)
	if s < 1 {
		s = 1
	}
	return strconv.FormatInt(s, 10)
}

const admitScript = `
local now = tonumber(ARGV[1])
local reserve_tokens = tonumber(ARGV[2]) or 0
local reserve_cost = tonumber(ARGV[3]) or 0
local scopes = cjson.decode(ARGV[4])
local function secs(us)
  if us < 0 then us = 0 end
  return math.max(1, math.ceil(us / 1000000))
end
local day = ARGV[5]
local month = ARGV[6]
local updates = {}
for _, s in ipairs(scopes) do
  local rpm = tonumber(s.rpm)
  local tpm = tonumber(s.tpm)
  local daily_req = tonumber(s.daily_requests)
  local daily_tok = tonumber(s.daily_tokens)
  local budget = tonumber(s.monthly_cost_usd)
  local id = s.id

  if rpm and rpm > 0 then
    local key = "gogate:rl:" .. id .. ":rpm"
    local interval = 60000000.0 / rpm
    local tat = tonumber(redis.call("GET", key) or tostring(now))
    local base = math.max(tat, now)
    local newtat = base + interval
    local capacity = rpm * interval
    if newtat - now > capacity then
      return {0, secs(newtat - capacity - now), id, "rpm"}
    end
    updates[#updates + 1] = {"set", key, newtat, 120000}
  end

  if tpm and tpm > 0 and reserve_tokens > 0 then
    local key = "gogate:rl:" .. id .. ":tpm"
    local interval = 60000000.0 / tpm
    local tat = tonumber(redis.call("GET", key) or tostring(now))
    local base = math.max(tat, now)
    local newtat = base + reserve_tokens * interval
    local capacity = 60000000.0
    if newtat - now > capacity then
      return {0, secs(newtat - capacity - now), id, "tpm"}
    end
    updates[#updates + 1] = {"set", key, newtat, 120000}
  end

  if daily_req and daily_req > 0 then
    local key = "gogate:q:" .. id .. ":dr:" .. day
    local v = tonumber(redis.call("GET", key) or "0") + 1
    if v > daily_req then
      return {0, 60, id, "daily_requests"}
    end
    updates[#updates + 1] = {"incr", key, 1, 172800}
  end

  if daily_tok and daily_tok > 0 and reserve_tokens > 0 then
    local key = "gogate:q:" .. id .. ":dt:" .. day
    local v = tonumber(redis.call("GET", key) or "0") + reserve_tokens
    if v > daily_tok then
      return {0, 60, id, "daily_tokens"}
    end
    updates[#updates + 1] = {"incr", key, reserve_tokens, 172800}
  end

  if budget and budget > 0 and reserve_cost > 0 then
    local key = "gogate:q:" .. id .. ":mc:" .. month
    local v = tonumber(redis.call("GET", key) or "0") + reserve_cost
    if v > budget then
      return {0, 3600, id, "monthly_budget"}
    end
    updates[#updates + 1] = {"incrfloat", key, reserve_cost, 5356800}
  end
end

for _, u in ipairs(updates) do
  if u[1] == "set" then
    redis.call("SET", u[2], tostring(u[3]), "PX", u[4])
  elseif u[1] == "incr" then
    redis.call("INCRBY", u[2], u[3])
    redis.call("EXPIRE", u[2], u[4])
  elseif u[1] == "incrfloat" then
    redis.call("INCRBYFLOAT", u[2], tostring(u[3]))
    redis.call("EXPIRE", u[2], u[4])
  end
end
return {1, 0, "", ""}
`

const settleScript = `
local now = tonumber(ARGV[1])
local token_delta = tonumber(ARGV[2]) or 0
local cost_delta = tonumber(ARGV[3]) or 0
local scopes = cjson.decode(ARGV[4])
local day = ARGV[5]
local month = ARGV[6]
for _, s in ipairs(scopes) do
  local tpm = tonumber(s.tpm)
  local id = s.id
  if tpm and tpm > 0 and token_delta ~= 0 then
    local key = "gogate:rl:" .. id .. ":tpm"
    local interval = 60000000.0 / tpm
    local tat = tonumber(redis.call("GET", key) or tostring(now))
    local newtat = math.max(now, tat + token_delta * interval)
    redis.call("SET", key, tostring(newtat), "PX", 120000)
  end
  if token_delta ~= 0 then
    local key = "gogate:q:" .. id .. ":dt:" .. day
    local v = tonumber(redis.call("GET", key) or "0") + token_delta
    if v < 0 then v = 0 end
    redis.call("SET", key, tostring(v), "EX", 172800)
  end
  if cost_delta ~= 0 then
    local key = "gogate:q:" .. id .. ":mc:" .. month
    local v = tonumber(redis.call("GET", key) or "0") + cost_delta
    if v < 0 then v = 0 end
    redis.call("SET", key, tostring(v), "EX", 5356800)
  end
end
return 1
`
