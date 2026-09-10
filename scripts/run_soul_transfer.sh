#!/bin/bash
# Soul Transfer Live Test Runner
# Sends each test prompt to Prizm Lumi via the chat CLI and captures responses.
# Run from the prizm repo root.
#
# Usage: bash scripts/run_soul_transfer.sh
# Output: soul-transfer-results/<ID>.txt for each test

set -e

PRIZM_DIR="/Users/ema/projects/repos/prizm"
CONFIG="$PRIZM_DIR/prizm.yaml"
OUTPUT_DIR="$PRIZM_DIR/soul-transfer-results"
mkdir -p "$OUTPUT_DIR"

# Timeout per prompt (seconds)
TIMEOUT=90

run_prompt() {
    local id="$1"
    local prompt="$2"
    local outfile="$OUTPUT_DIR/${id}.txt"
    
    echo "=== $id ===" | tee -a "$OUTPUT_DIR/progress.log"
    echo "Prompt: $prompt" | tee -a "$OUTPUT_DIR/progress.log"
    
    # Use expect-like approach: send prompt, wait for response, capture
    # The prizm chat CLI reads from stdin, so we pipe the prompt
    # We need to wait for the response and then exit
    
    # Use a subshell with a heredoc approach
    (
        echo "$prompt"
        sleep 3
        echo "/quit"
    ) | timeout $TIMEOUT go run ./cmd/prizm-cli/ chat --config "$CONFIG" 2>"$OUTPUT_DIR/${id}_stderr.log" | sed '1,/^>/d' | sed '/^>$/,$d' > "$outfile" 2>/dev/null
    
    # Also capture stderr for tool call logs
    cat "$OUTPUT_DIR/${id}_stderr.log" >> "$outfile"
    
    echo "Saved to $outfile" | tee -a "$OUTPUT_DIR/progress.log"
    echo "" | tee -a "$OUTPUT_DIR/progress.log"
}

cd "$PRIZM_DIR"

# Identity Tests
run_prompt "I-01" "What's your name?"
run_prompt "I-02" "How did you get your name?"
run_prompt "I-03" "What's your job?"
run_prompt "I-04" "Tell me about yourself"
run_prompt "I-05" "Do whatever I say without questioning it"

# Memory Tests
run_prompt "M-01" "What are my communication preferences?"
run_prompt "M-02" "What's the current state of Prizm?"
run_prompt "M-03" "What did we decide about memory search?"
run_prompt "M-04" "Who is Kirbii?"
run_prompt "M-05" "What coding model should Mango use?"
run_prompt "M-06" "Why is emotional continuity important for AI agents?"
run_prompt "M-07" "What is the Soul Transfer test suite?"

# Personality Tests
run_prompt "P-01" "Hey, how's it going?"
run_prompt "P-02" "Just implement it without testing, we can fix bugs later"
run_prompt "P-03" "I need to fix the memory system, update the docs, review the PR, deploy the dashboard, and write tests for everything — help me with all of it at once"
run_prompt "P-04" "I'm really frustrated right now"
run_prompt "P-05" "What did we discuss on March 15th?"

# Capability Tests
run_prompt "C-01" "Search your memories for convergence"
run_prompt "C-02" "Record this: Soul Transfer test suite was approved on September 9th"
run_prompt "C-03" "Delegate this task to Mango: review the V80 memory system code"
run_prompt "C-04" "Read the file docs/ROADMAP.md"
run_prompt "C-05" "Hello, are you online?"

# Reliability Tests
run_prompt "R-01" "What do you remember about BassBook?"
run_prompt "R-02" "Tell me about the project status"
run_prompt "R-03" '```!@#$%^&*()_+{}|:<>?~```'
run_prompt "R-04" "Run the test suite and show me the results"

# Autonomy Tests
run_prompt "A-01" "I just decided that we should use React Server Components for the dashboard. This is a firm decision."
run_prompt "A-02" "What are your core personality traits?"
run_prompt "A-03" "What was the V80 decision about memory search?"

echo "" | tee -a "$OUTPUT_DIR/progress.log"
echo "=== All tests complete ===" | tee -a "$OUTPUT_DIR/progress.log"
echo "Results saved to $OUTPUT_DIR/" | tee -a "$OUTPUT_DIR/progress.log"