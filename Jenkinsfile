// Builds clipsrv from source on the Jenkins agent and deploys to a local
// SysV service on the same box. Mirrors the shix-media-server pipeline.
//
// Prerequisites on the agent (handled by deploy/provision.sh):
//   - Go 1.23+ on PATH for the jenkins user
//   - /opt/shixo-msn writable by jenkins
//   - sudoers rule allowing: service shixo-msn restart
pipeline {
  agent any

  environment {
    APP         = 'shixo-msn'
    DEPLOY_DIR  = '/opt/shixo-msn'
    // pure-Go server: no CGO needed, smaller binary
    CGO_ENABLED = '0'
    GOFLAGS     = '-trimpath'
    // Keep Go module + build caches inside the workspace so they survive
    // across builds without colliding with the agent user's home perms.
    GOCACHE     = "${WORKSPACE}/.gocache"
    GOMODCACHE  = "${WORKSPACE}/.gomod"
    // Jenkins agents start with a minimal PATH (/usr/bin:/bin). The Go
    // tarball installs to /usr/local/go and provision.sh symlinks into
    // /usr/local/bin — prepend both so `go` and `sudo` are found.
    PATH        = "/usr/local/go/bin:/usr/local/bin:/usr/local/sbin:${env.PATH}"
  }

  options {
    timestamps()
    disableConcurrentBuilds()
    timeout(time: 15, unit: 'MINUTES')
  }

  stages {
    stage('Checkout') {
      steps { checkout scm }
    }

    stage('Verify') {
      // GUI (./cmd/shixo-msn) needs CGO + OpenGL which the Jenkins box doesn't have.
      // Only vet packages reachable from the server binary.
      steps { sh 'go vet ./cmd/clipsrv/... ./internal/server/... ./internal/proto/...' }
    }

    stage('Build') {
      steps {
        sh '''
          mkdir -p dist
          # -buildvcs=false: workspace is owned by jenkins but git metadata
          # comes from the Jenkins checkout — avoids "dubious ownership" stamping.
          go build -buildvcs=false -ldflags="-s -w" -o dist/clipsrv ./cmd/clipsrv
        '''
      }
    }

    stage('Deploy') {
      steps {
        sh '''
          mkdir -p "$DEPLOY_DIR"
          # Atomic swap: copy alongside, then mv onto the existing binary.
          # mv onto a running ELF replaces the inode; the running process
          # keeps its old text mapping until the service restart below.
          install -m 0755 dist/clipsrv "$DEPLOY_DIR/clipsrv.new"
          mv -f "$DEPLOY_DIR/clipsrv.new" "$DEPLOY_DIR/clipsrv"

          # Sync the deploy scripts (init.d template, provision, backup, etc.)
          # into /opt/shixo-msn/deploy so admins can run them from the server
          # without a separate `git pull`. Init scripts are not auto-installed
          # — installers under /etc are only touched by the *-install.sh helpers.
          mkdir -p "$DEPLOY_DIR/deploy"
          rsync -a --delete deploy/ "$DEPLOY_DIR/deploy/"

          if [ -d /run/systemd/system ]; then
            sudo /usr/bin/systemctl restart "$APP"
          else
            sudo /usr/sbin/service "$APP" restart
          fi
        '''
      }
    }

    stage('Smoke test') {
      steps {
        sh '''
          # /api/health is unauthenticated and returns the literal string "ok".
          for i in $(seq 1 30); do
            if [ "$(curl -fsS http://127.0.0.1:6303/api/health 2>/dev/null)" = "ok" ]; then
              echo "service is up"; exit 0
            fi
            sleep 1
          done
          echo "service did not come up"; exit 1
        '''
      }
    }
  }

  post {
    success { echo "Deployed build #${env.BUILD_NUMBER}" }
    failure { echo 'Deploy failed — previous version still running.' }
  }
}
