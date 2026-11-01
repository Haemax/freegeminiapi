
Write-Host "Server Test" -ForegroundColor Cyan

# Test health
$health = Invoke-RestMethod -Uri "http://127.0.0.1:8001/health" -Method GET
Write-Host "Health: $($health.status)" -ForegroundColor Green

# Test worker pool
$workers = Invoke-RestMethod -Uri "http://127.0.0.1:8001/v1/workers" -Method GET
Write-Host "Workers: $($workers.total_workers) total, $($workers.active_workers) active" -ForegroundColor Green

# Test chat
$body = @{
    model = "gemini-3.8-flash"
    messages = @(@{ role = "user"; content = "Hello" })
    stream = $false
} | ConvertTo-Json -Depth 3

$response = Invoke-RestMethod -Uri "http://127.0.0.1:8001/v1/chat/completions" -Method POST -Body $body -ContentType "application/json" -TimeoutSec 30
Write-Host "Response: $($response.choices[0].message.content)" -ForegroundColor Green
Write-Host "Done!" -ForegroundColor Cyan
