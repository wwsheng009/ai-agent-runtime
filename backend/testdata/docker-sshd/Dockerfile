FROM alpine:3.20

RUN apk add --no-cache openssh-server openssh-client && \
    mkdir -p /run/sshd && \
    ssh-keygen -A

# 创建测试用户 testuser，密码 testpass
RUN adduser -D -s /bin/sh testuser && \
    echo 'testuser:testpass' | chpasswd

# 允许密码认证
RUN sed -i 's/#PasswordAuthentication yes/PasswordAuthentication yes/' /etc/ssh/sshd_config && \
    sed -i 's/#PermitRootLogin prohibit-password/PermitRootLogin no/' /etc/ssh/sshd_config && \
    echo 'KbdInteractiveAuthentication yes' >> /etc/ssh/sshd_config

# 证书认证：信任 CA（TrustedUserCAKeys），允许 CA 签发的用户证书登录
COPY keys/ca_ed25519.pub /etc/ssh/ca_ed25519.pub
RUN chmod 644 /etc/ssh/ca_ed25519.pub && \
    echo 'TrustedUserCAKeys /etc/ssh/ca_ed25519.pub' >> /etc/ssh/sshd_config && \
    echo 'PubkeyAuthentication yes' >> /etc/ssh/sshd_config

# 创建测试数据目录
RUN mkdir -p /home/testuser/data && \
    echo 'hello from remote container' > /home/testuser/data/hello.txt && \
    mkdir -p /home/testuser/data/subdir && \
    echo 'nested file' > /home/testuser/data/subdir/nested.txt && \
    chown -R testuser:testuser /home/testuser

# 注入测试公钥（构建期设置正确属主，避免运行时 chown）
COPY keys/testuser_ed25519.pub /home/testuser/.ssh/authorized_keys
RUN chmod 700 /home/testuser/.ssh && \
    chmod 600 /home/testuser/.ssh/authorized_keys && \
    chown -R testuser:testuser /home/testuser/.ssh

EXPOSE 22

CMD ["/usr/sbin/sshd", "-D", "-e"]
