Write-Host "Starting Go backend with debugger..."
dlv debug ./main.go --headless --listen=:2345 --api-version=2 --accept-multiclient
