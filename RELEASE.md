# Release Checklist

**⚠️ CRITICAL: STOP IMMEDIATELY IF ANY STEP FAILS ⚠️**

If any command returns an error or fails quality checks, STOP the release process immediately. Fix the issue, commit the fix, and restart from step 1.

## Pre-Release Requirements

Before starting the release process, ensure:

1. **Working Directory is Clean**
   - Run `git status` to verify no uncommitted changes
   - The version will show as "-dirty" if there are uncommitted changes, breaking the release
   - If any uncommitted changes exist, use `git stash` to temporarily store them

2. **All Source Files Are Included in Dockerfile**
   - Verify `Dockerfile` copies all necessary `.go` files
   - If the project has multiple source files (e.g., `main.go`, `discovery.go`), ensure the Dockerfile uses `COPY *.go ./` instead of `COPY main.go ./`
   - The `.dockerignore` already excludes `*_test.go` files

3. **Documentation is Updated**
   - Update `CHANGELOG.md` with new version section and all changes
   - Update `README.md` if new features require documentation
   - Update `CLAUDE.md` if process documentation needs changes
   - Commit and push documentation updates BEFORE creating the release tag

## Release Process

Follow these steps in order. Do NOT skip ahead or the release will fail.

### Step 1: Create and Push Release Tag

```bash
# Create the version tag
git tag v0.X.X

# Push the tag to GitHub (this triggers the release process)
git push origin v0.X.X
```

**CRITICAL:** Once you push this tag, all subsequent builds MUST have a clean working directory or the version will be "v0.X.X-N-gHASH" instead of "v0.X.X".

### Step 2: Build and Push Multi-Platform Docker Images

Build images for both AMD64 and ARM64 architectures:

```bash
# Build AMD64 image with version
docker build --platform linux/amd64 --build-arg VERSION=v0.X.X -t astrostl/pentameter:latest-amd64 -t astrostl/pentameter:v0.X.X-amd64 .

# Build ARM64 image with version
docker build --platform linux/arm64 --build-arg VERSION=v0.X.X -t astrostl/pentameter:latest-arm64 -t astrostl/pentameter:v0.X.X-arm64 .

# Push all images
docker push astrostl/pentameter:latest-amd64
docker push astrostl/pentameter:v0.X.X-amd64
docker push astrostl/pentameter:latest-arm64
docker push astrostl/pentameter:v0.X.X-arm64
```

**CRITICAL:**
- The `--build-arg VERSION=v0.X.X` is REQUIRED to inject the version into the Docker image. Without it, the binary will show "pentameter dev" instead of the correct version.
- Do NOT use `make docker-push` initially, as it depends on a local `pentameter:latest` image that may not exist. Build images manually first.

### Step 3: Create Multi-Platform Manifests

Create manifests that automatically select the correct architecture:

```bash
# Ensure manifest-tool is installed and in PATH
export PATH=$PATH:$(go env GOPATH)/bin

# Create manifest for latest
manifest-tool push from-args \
  --platforms linux/amd64,linux/arm64 \
  --template astrostl/pentameter:latest-ARCHVARIANT \
  --target astrostl/pentameter:latest

# Create manifest for version tag
manifest-tool push from-args \
  --platforms linux/amd64,linux/arm64 \
  --template astrostl/pentameter:v0.X.X-ARCHVARIANT \
  --target astrostl/pentameter:v0.X.X
```

### Step 4: Build Homebrew Assets

Build macOS binaries and generate checksums:

```bash
# Clean any previous builds
rm -rf dist

# Build binaries, package them, and generate checksums
make build-macos-binaries package-macos-binaries generate-macos-checksums
```

**CRITICAL:** The working directory MUST still be clean at this point. If you've made any commits since creating the tag, the binaries will have a dirty version.

### Step 5: Verify Binary Versions

Before creating the GitHub release, verify the binaries have the correct clean version:

```bash
# Check AMD64 version
./dist/pentameter-darwin-amd64 --version

# Check ARM64 version
./dist/pentameter-darwin-arm64 --version
```

Both should show exactly `pentameter v0.X.X` with NO suffixes like `-dirty` or `-N-gHASH`.

**If versions are wrong:** You made commits after creating the tag. You MUST:
1. Move the tag: `git tag -f v0.X.X && git push -f origin v0.X.X`
2. Rebuild from Step 2 (Docker images will need the updated tag)
3. Rebuild Homebrew assets: `rm -rf dist && make build-macos-binaries package-macos-binaries generate-macos-checksums`

### Step 6: Record Checksums for Formula

**CRITICAL:** Save these checksums NOW - you'll need them in Step 8:

```bash
# Display and save checksums
cat dist/checksums.txt
```

**Copy these SHA256 values to a text file or leave this terminal window open.** The Homebrew formula MUST use these EXACT checksums that match the GitHub release assets.

### Step 7: Create GitHub Release

Create the GitHub release with the generated assets:

```bash
gh release create v0.X.X \
  --title "v0.X.X - Release Title" \
  --notes "## Added
- Feature 1
- Feature 2

## Changed
- Change 1
- Change 2

🐳 **Docker Images Available:**
- Multi-platform support (AMD64 + ARM64)
- \`docker pull astrostl/pentameter:v0.X.X\`
- \`docker pull astrostl/pentameter:latest\`

🍺 **Homebrew Installation:**
\`\`\`bash
brew install astrostl/pentameter/pentameter
\`\`\`

Generated with [Claude Code](https://claude.com/claude-code)" \
  dist/pentameter-v0.X.X-darwin-amd64.tar.gz \
  dist/pentameter-v0.X.X-darwin-arm64.tar.gz \
  dist/checksums.txt
```

**IMPORTANT:** GitHub may take a few seconds to process the assets. Wait until the release page loads before proceeding.

### Step 8: Update Homebrew Formula with Correct Checksums

**CRITICAL:** Manually update the formula with the checksums you saved in Step 6. DO NOT run `make update-homebrew-formula` as it will rebuild binaries with different checksums.

```bash
# Edit the formula manually
nano Formula/pentameter.rb
# OR use your preferred editor
code Formula/pentameter.rb
```

Update these two lines with the checksums from Step 6:
- Line 9: ARM64 sha256 (for `darwin-arm64.tar.gz`)
- Line 12: AMD64 sha256 (for `darwin-amd64.tar.gz`)

**Verify the checksums match GitHub release:**

```bash
# Download and verify ARM64 checksum from GitHub
curl -sL https://github.com/astrostl/pentameter/releases/download/v0.X.X/pentameter-v0.X.X-darwin-arm64.tar.gz | shasum -a 256

# Download and verify AMD64 checksum from GitHub
curl -sL https://github.com/astrostl/pentameter/releases/download/v0.X.X/pentameter-v0.X.X-darwin-amd64.tar.gz | shasum -a 256

# Compare with formula
grep sha256 Formula/pentameter.rb
```

All three sources (Step 6 checksums, GitHub download checksums, formula checksums) MUST match exactly.

### Step 9: Commit and Push Formula

Commit the formula with correct checksums:

```bash
git add Formula/pentameter.rb
git commit -m "Update Homebrew formula for v0.X.X with correct SHA256 checksums"
git push origin master
```

**CRITICAL:** Do NOT move the release tag after this point. The GitHub release already has the correct binaries.

### Step 10: Verify Homebrew Installation

Test that the formula works correctly:

```bash
# Update Homebrew to get latest formula
brew update

# Clear any cached downloads (in case of previous failed attempts)
rm -f ~/Library/Caches/Homebrew/downloads/*pentameter-v0.X.X*

# Upgrade pentameter
brew upgrade pentameter

# Verify version
pentameter --version
```

Should show: `pentameter v0.X.X` (clean version, no suffixes)

If you get a SHA256 mismatch error:
1. The checksums in the formula don't match the GitHub release assets
2. Download one of the release assets manually and verify its checksum: `shasum -a 256 pentameter-v0.X.X-darwin-arm64.tar.gz`
3. Update the formula with the correct checksum and repeat Step 9

## Common Issues and Solutions

### Issue: Docker Build Fails with "undefined: FunctionName"

**Cause:** Dockerfile only copies `main.go` but project has multiple source files.

**Solution:** Update Dockerfile to use `COPY *.go ./` instead of `COPY main.go ./`. Commit and push this fix, then restart from Step 1.

### Issue: Binary Version Shows "v0.X.X-1-gHASH"

**Cause:** Commits were made after creating the release tag.

**Solution:**
1. Move the tag: `git tag -f v0.X.X && git push -f origin v0.X.X`
2. Rebuild everything from Step 2 onward
3. The Docker images and Homebrew binaries MUST be rebuilt with the updated tag

### Issue: Binary Version Shows "v0.X.X-dirty"

**Cause:** Uncommitted changes in working directory.

**Solution:**
1. Commit or stash all changes
2. Move the tag: `git tag -f v0.X.X && git push -f origin v0.X.X`
3. Rebuild everything from Step 2 onward

### Issue: Homebrew SHA256 Mismatch

**Cause:** The checksums in the formula don't match the GitHub release assets.

**Root Cause:** Running `make update-homebrew-formula` in Step 8 rebuilds binaries with different checksums than what was uploaded to GitHub.

**Solution:**
1. Download a release asset from GitHub and verify its checksum:
   ```bash
   curl -sL https://github.com/astrostl/pentameter/releases/download/v0.X.X/pentameter-v0.X.X-darwin-arm64.tar.gz | shasum -a 256
   ```
2. Manually edit `Formula/pentameter.rb` with the correct checksum
3. DO NOT use `make update-homebrew-formula` - it rebuilds binaries
4. Commit and push the formula fix
5. Clear Homebrew cache: `rm -f ~/Library/Caches/Homebrew/downloads/*pentameter*`
6. Test: `brew upgrade pentameter`

### Issue: "make docker-push" Fails with "not found"

**Cause:** The Makefile target expects a local `pentameter:latest` image to exist.

**Solution:** Don't use `make docker-push` for releases. Build images manually using `docker build` commands as shown in Step 2.

### Issue: Docker Image Shows "pentameter dev" Instead of Version

**Cause:** Docker images were built without the `--build-arg VERSION=v0.X.X` flag.

**Solution:**
1. Rebuild Docker images with the `--build-arg VERSION=v0.X.X` flag (see Step 2)
2. Push the corrected images
3. Recreate the multi-platform manifests
4. Verify: `docker run --rm astrostl/pentameter:v0.X.X --version` should show `pentameter v0.X.X`

**Why this happens:** The `.dockerignore` excludes the `.git` directory, so the Dockerfile can't run `git describe` to get the version. The version must be passed in from outside where git is available.

## Post-Release Verification

After completing all steps, verify:

1. ✅ Docker Hub has images for both `v0.X.X` and `latest` tags with multi-platform manifests
2. ✅ GitHub release exists with correct binaries and checksums
3. ✅ Homebrew upgrade succeeds without checksum errors
4. ✅ Installed binary shows clean version: `pentameter v0.X.X`
5. ✅ Formula in repository has correct checksums matching GitHub release assets

## Release Checklist Summary

Use this checklist to track progress:

- [ ] Working directory is clean (`git status`)
- [ ] Documentation updated (CHANGELOG.md, README.md)
- [ ] Dockerfile copies all necessary source files
- [ ] Documentation committed and pushed
- [ ] Release tag created and pushed (`git tag v0.X.X && git push origin v0.X.X`)
- [ ] Docker AMD64 image built and pushed
- [ ] Docker ARM64 image built and pushed
- [ ] Multi-platform manifests created (latest and version)
- [ ] Homebrew binaries built (`rm -rf dist && make build-macos-binaries package-macos-binaries generate-macos-checksums`)
- [ ] Binary versions verified (no -dirty or -hash suffixes)
- [ ] Checksums saved from `dist/checksums.txt` (copy to text file or keep terminal open)
- [ ] GitHub release created with assets
- [ ] Homebrew formula manually edited with checksums from Step 6
- [ ] Formula checksums verified against GitHub release downloads
- [ ] Formula committed and pushed
- [ ] Homebrew installation tested successfully
- [ ] Installed version verified (`pentameter --version`)

## Notes

- The entire release process should take about 10-15 minutes if everything goes smoothly
- Most issues come from committing changes after creating the release tag
- Always verify binary versions before creating the GitHub release
- The Homebrew formula checksums MUST match the GitHub release assets exactly
- Never move the release tag after creating the GitHub release (the assets are already uploaded)
