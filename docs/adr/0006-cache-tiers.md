# ADR 0006: Cache tiers

## Статус

Принято.

## Контекст

Повторяющиеся non-streaming запросы можно обслуживать быстрее и дешевле.
Нужны точный кэш и опциональный semantic cache, при этом streaming нельзя
кэшировать без буферизации.

## Решение

- **Exact** (Redis): только non-streaming. Ключ — tenant, model, messages,
  temperature, max_tokens и другие параметры генерации. TTL.
- **Semantic** (pgvector): opt-in, только non-streaming. Embedding промпта,
  cosine similarity с порогом, изоляция tenant+model.
- Streaming всегда `BYPASS`.
- Кэш-хит возвращает сохранённый usage, но не списывает upstream-токены:
  резерв лимитов возвращается.

## Последствия

- Redis нужен для exact cache; semantic cache требует pgvector и embedder.
- Ключ включает дополнительные параметры (top_p/stop/seed), чтобы не отдать
  неверный ответ.
- Semantic cache может вернуть близкий, но не идентичный ответ; порог и embedder
  влияют на качество.
