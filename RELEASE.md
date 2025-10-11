# Release Checklist

Follow these steps to cut a new release:

1. **Update Documentation**
   - Update `CHANGELOG.md`: Add new version section with all changes since last release
   - Update `README.md`: Reflect any new features, configuration changes, or installation updates
   - Update `CLAUDE.md`: Document any process changes, new development patterns, or architectural updates
   - Update `API.md`: Document any new IntelliCenter API findings or endpoint discoveries (if applicable)
   - **Commit documentation changes**: `git add CHANGELOG.md README.md CLAUDE.md API.md && git commit -m "Update documentation for vX.X.X release" && git push`

2. **Run Quality Checks**
   - Execute `make quality-comprehensive` to ensure all checks pass with maximum linter coverage before release

3. **Build Binary**
   - Run `make build` to create a clean production binary
   - Verify the build completes successfully without errors

4. **Ensure Clean Working Directory**
   - Run `git status` to verify no uncommitted changes (version will show as "-dirty" otherwise)
   - If any uncommitted changes exist, use `git stash` to temporarily store them

5. **Create Release Tag**
   - Create version tag: `git tag vX.X.X`
   - Push tag to trigger release process: `git push origin vX.X.X`

6. **Build and Push Multi-Platform Docker Images**
   - Run: `make docker-push`
   - This automatically builds AMD64 and ARM64 images, pushes them to DockerHub, and creates multi-platform manifests

7. **Build Homebrew Assets**
   - Run: `make build-macos-binaries package-macos-binaries generate-macos-checksums update-homebrew-formula`
   - This generates binaries and checksums in `dist/` directory

8. **Create GitHub Release**
   - Use `gh release create` with generated assets from `dist/`
   - Include release notes from CHANGELOG.md

9. **Verify Homebrew Formula**
   - Push updated Formula/pentameter.rb with correct SHA256 checksums
   - Test with `brew upgrade` to ensure no checksum mismatches

See `CLAUDE.md` for detailed troubleshooting and complete workflow documentation.
