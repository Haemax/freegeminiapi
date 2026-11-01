# Comprehensive API Test Script
Write-Host "==================================" -ForegroundColor Cyan
Write-Host "Free Gemini API - Test Suite" -ForegroundColor Cyan
Write-Host "==================================" -ForegroundColor Cyan

# Create test image
Write-Host ""
Write-Host "[1/5] Creating test image..." -ForegroundColor Yellow
$pngBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAoAAAAKCAYAAACNMs+9AAAAFUlEQVR42mP8/5+hnoEIwDiqkL4KAcT9GO0U4BxoAAAAAElFTkSuQmCC"
$pngBytes = [Convert]::FromBase64String($pngBase64)
$testImagePath = "test_image.png"
[System.IO.File]::WriteAllBytes($testImagePath, $pngBytes)
Write-Host "  Created 10x10 red PNG image" -ForegroundColor Green

# Test 1: Health check
Write-Host ""
Write-Host "[2/5] Testing health endpoint..." -ForegroundColor Yellow
try {
    $health = Invoke-RestMethod -Uri "http://127.0.0.1:8001/health" -Method GET -TimeoutSec 5
    Write-Host "  Status: $($health.status)" -ForegroundColor Green
    Write-Host "  Extension workers: $($health.active_extension_workers)" -ForegroundColor Green
    Write-Host "  Cookie accounts: $($health.cookie_accounts_in_pool)" -ForegroundColor Green
} catch {
    Write-Host "  FAILED: $_" -ForegroundColor Red
}

# Test 2: Worker pool status (/v1/workers)
Write-Host ""
Write-Host "[3/5] Testing worker pool (/v1/workers)..." -ForegroundColor Yellow
try {
    $workers = Invoke-RestMethod -Uri "http://127.0.0.1:8001/v1/workers" -Method GET -TimeoutSec 5
    Write-Host "  Total workers: $($workers.total_workers)" -ForegroundColor Green
    Write-Host "  Active workers: $($workers.active_workers)" -ForegroundColor Green
    Write-Host "  In-flight: $($workers.total_in_flight)" -ForegroundColor Green
    foreach($w in $workers.workers) {
        Write-Host "    - Worker [$($w.id)]: Tier=$($w.tier), Status=$($w.status), Served=$($w.total_served)" -ForegroundColor Gray
    }
} catch {
    Write-Host "  FAILED: $_" -ForegroundColor Red
}

# Test 3: OpenAI Chat Completion (/v1/chat/completions)
Write-Host ""
Write-Host "[4/5] Testing OpenAI chat completion (/v1/chat/completions)..." -ForegroundColor Yellow
$chatBody = @{
    model = "gemini-3.8-flash"
    messages = @(
        @{ role = "user"; content = "Say exactly: Hello World" }
    )
    stream = $false
} | ConvertTo-Json -Depth 3

try {
    $response = Invoke-RestMethod -Uri "http://127.0.0.1:8001/v1/chat/completions" -Method POST -Body $chatBody -ContentType "application/json" -TimeoutSec 30
    if ($response.choices) {
        Write-Host "  Response: $($response.choices[0].message.content)" -ForegroundColor Green
    } elseif ($response.text) {
        Write-Host "  Response: $($response.text)" -ForegroundColor Green
    } else {
        Write-Host "  Response: $($response | ConvertTo-Json -Compress)" -ForegroundColor Green
    }
} catch {
    Write-Host "  FAILED: $_" -ForegroundColor Red
}

# Test 4: Image upload via /chat
Write-Host ""
Write-Host "[5/5] Testing image upload via /chat..." -ForegroundColor Yellow
try {
    $curlOutput = curl.exe -s -X POST "http://127.0.0.1:8001/chat" -F "prompt=What color is this image?" -F "model=gemini-3.8-flash" -F "stream=false" -F "images=@test_image.png"
    Write-Host "  Raw response: $curlOutput" -ForegroundColor Green
} catch {
    Write-Host "  FAILED: $_" -ForegroundColor Red
}

Write-Host ""
Write-Host "=================================="
Write-Host "Test Suite Complete!"
Write-Host "=================================="
