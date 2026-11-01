# Comprehensive Image Upload Test for Free Gemini API
Write-Host "==================================" -ForegroundColor Cyan
Write-Host "Image Upload Test Suite" -ForegroundColor Cyan
Write-Host "==================================" -ForegroundColor Cyan

# Test 1: Non-streaming with image via OpenAI format
Write-Host ""
Write-Host "[1/2] Testing non-streaming image upload via OpenAI format..." -ForegroundColor Yellow

$base64Image = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAoAAAAKCAYAAACNMs+9AAAAFUlEQVR42mP8/5+hnoEIwDiqkL4KAcT9GO0U4BxoAAAAAElFTkSuQmCC"

$body = @{
    model = "gemini-3.8-flash"
    messages = @(
        @{
            role = "user"
            content = @(
                @{ type = "text"; text = "What color is this image?" }
                @{ type = "image_url"; image_url = @{ url = $base64Image } }
            )
        }
    )
    stream = $false
} | ConvertTo-Json -Depth 10

try {
    $response = Invoke-RestMethod -Uri "http://127.0.0.1:8001/v1/chat/completions" -Method POST -Body $body -ContentType "application/json" -TimeoutSec 60
    Write-Host "  Image upload successful!" -ForegroundColor Green
    Write-Host "  Response: $($response.choices[0].message.content)" -ForegroundColor Green
} catch {
    Write-Host "  FAILED: $_" -ForegroundColor Red
}

Write-Host ""
Write-Host "Test Suite Complete!" -ForegroundColor Cyan
