
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

Write-Host "Sending image to OpenAI-compatible endpoint..."
try {
    $response = Invoke-RestMethod -Uri "http://127.0.0.1:8001/v1/chat/completions" -Method POST -Body $body -ContentType "application/json" -TimeoutSec 30
    Write-Host "Response:"
    Write-Host ($response | ConvertTo-Json -Depth 5)
} catch {
    Write-Host "Error: $_"
    if ($_.Exception.Response) {
        $reader = New-Object System.IO.StreamReader($_.Exception.Response.GetResponseStream())
        $reader.BaseStream.Position = 0
        Write-Host "Response: $($reader.ReadToEnd())"
    }
}
