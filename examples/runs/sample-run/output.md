# Prizm Event Lifecycle

1. **Task Created** — A new task enters the system
2. **Task Started** — Processing begins
3. **LLM Requested** — The model is called
4. **LLM Completed** — The model returns a result
5. **Output Written** — The result is saved to disk
6. **Task Completed** — The task finishes successfully

Each event carries a `parent_id` linking it to its direct causal predecessor, forming a traceable chain.