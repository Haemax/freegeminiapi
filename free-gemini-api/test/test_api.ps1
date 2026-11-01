# Test script for Free Gemini API
Write-Host "Testing Free Gemini API..." -ForegroundColor Cyan

# Test 1: Health check
Write-Host "1. Testing health endpoint..." -ForegroundColor Yellow
try {
    $health = Invoke-RestMethod -Uri "http://127.0.0.1:8001/health" -Method GET
    Write-Host "Health check passed" -ForegroundColor Green
} catch {
    Write-Host "Health check failed: $_" -ForegroundColor Red
    exit 1
}

# Test 2: Worker status
Write-Host "2. Testing worker pool..." -ForegroundColor Yellow
try {
    $workers = Invoke-RestMethod -Uri "http://127.0.0.1:8001/workers" -Method GET
    Write-Host "Total workers: $($workers.total_workers)" -ForegroundColor Green
    Write-Host "Active workers: $($workers.active_workers)" -ForegroundColor Green
} catch {
    Write-Host "Worker pool test failed: $_" -ForegroundColor Red
}

# Test 3: Simple chat
Write-Host "3. Testing chat completion..." -ForegroundColor Yellow
$chatBody = @{
    model = "gemini-3.8-flash"
    prompt = "Say hello"
    stream = $false
} | ConvertTo-Json

try {
    $response = Invoke-RestMethod -Uri "http://127.0.0.1:8001/v1/chat/completions" -Method POST -Body $chatBody -ContentType "application/json" -TimeoutSec 30
    Write-Host "Chat response received: $($response.text.Substring(0, 50))..." -ForegroundColor Green
} catch {
    Write-Host "Chat test failed: $_" -ForegroundColor Red
}

Write-Host "Tests completed!" -ForegroundColor Cyan
