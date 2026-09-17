#!/bin/bash
# Ingest all memory/*.md files into Remembrance (Recall) vector store
# Usage: bash ingest-memory.sh /path/to/memory/dir

MEM_DIR="${1:-/Users/ema/projects/repos/prizm/prizm-workspace/memory}"
REM_URL="http://127.0.0.1:18790/v1/memory/ingest"
COUNT=0
FAILED=0

echo "Ingesting memory files from: $MEM_DIR"
echo "---"

for f in "$MEM_DIR"/*.md; do
  [ -f "$f" ] || continue
  
  filename=$(basename "$f")
  content=$(cat "$f")
  
  # Skip empty or very small files
  if [ ${#content} -lt 50 ]; then
    echo "  SKIP (too small): $filename"
    continue
  fi
  
  # Truncate content to 4000 chars for ingestion
  if [ ${#content} -gt 4000 ]; then
    content="${content:0:4000}..."
  fi
  
  # Create a title from the filename
  title="memory/$filename"
  
  # POST to Remembrance
  response=$(curl -s -X POST "$REM_URL" \
    -H "Content-Type: application/json" \
    -d "{
      \"scope\": \"project\",
      \"category\": \"memory\",
      \"title\": \"$title\",
      \"summary\": \"Memory file: $filename\",
      \"content\": $(echo "$content" | python3 -c "import sys,json; print(json.dumps(sys.stdin.read()))"),
      \"source_type\": \"agent\",
      \"source_agent\": \"lumi\",
      \"importance_score\": 0.8,
      \"project_id\": \"prizm\"
    }" 2>/dev/null)
  
  if [ -n "$response" ]; then
    echo "  OK: $filename"
    ((COUNT++))
  else
    echo "  FAIL: $filename"
    ((FAILED++))
  fi
  
  # Small delay to avoid overwhelming the server
  sleep 0.1
done

echo "---"
echo "Done: $COUNT ingested, $FAILED failed"