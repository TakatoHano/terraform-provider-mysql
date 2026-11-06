---
page_title: "Provider: MySQL (TakatoHano fork)"
description: |-
  A provider for MySQL Server, with port forwarding through AWS SSM Session Manager or a public bastion.
---

# MySQL (TakatoHano fork) Provider

[MySQL](http://www.mysql.com) is a relational database server. The MySQL
provider exposes resources used to manage the configuration of resources
in a MySQL server.

This is an unofficial fork of
[hashicorp/terraform-provider-mysql](https://github.com/hashicorp/terraform-provider-mysql)
that adds port forwarding through
[AWS SSM Session Manager](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager.html)
and through a public bastion.

Use the navigation to the left to read about the available resources.

## Example Usage

The following is a minimal example:

```terraform
# Configure the MySQL provider
provider "mysql" {
  endpoint = "my-database.example.com:3306"
  username = "app-user"
  password = "app-password"
}

# Create a Database
resource "mysql_database" "app" {
  name = "my_awesome_app"
}
```

This provider can be used in conjunction with other resources that create
MySQL servers. For example, `aws_db_instance` is able to create MySQL
servers in Amazon's RDS service.

```terraform
# Create a database server
resource "aws_db_instance" "default" {
  engine         = "mysql"
  engine_version = "5.6.17"
  instance_class = "db.t1.micro"
  name           = "initial_db"
  username       = "rootuser"
  password       = "rootpasswd"

  # etc, etc; see aws_db_instance docs for more
}

# Configure the MySQL provider based on the outcome of
# creating the aws_db_instance.
provider "mysql" {
  endpoint = aws_db_instance.default.endpoint
  username = aws_db_instance.default.username
  password = aws_db_instance.default.password
}

# Create a second database, in addition to the "initial_db" created
# by the aws_db_instance resource above.
resource "mysql_database" "app" {
  name = "another_db"
}
```

## SOCKS5 Proxy Support

The MySQL provider respects the `ALL_PROXY` and/or `all_proxy` environment variables.

```shell
export all_proxy="socks5://your.proxy:3306"
```

## Port Forward with AWS SSM Session Manager Support

~> **Caution:** This is a feature in development.

The provider can reach a database in a private subnet through an EC2 instance
that runs the SSM Agent. Set `endpoint` to `localhost:<unused port>` and
configure `aws_ssm_session_manager_client_config`.

The recommended `mode = "port_forwarding"` uses the SSM document
`AWS-StartPortForwardingSessionToRemoteHost`. It needs no SSH keys, no sshd on
the instance and no `ssh_user`/`ssh_key_path`. `rds_endpoint` may be given as
`host` (port 3306 is assumed) or `host:port`.

~> **Note:** `port_forwarding` is experimental: it is covered by offline tests only and has not yet been verified against real AWS; please report issues. The default `ssh` mode is the long-standing, production-tested path.

```terraform
# Create VPC and Subnets
module "vpc" {

  # (sample value)
  public_subnets   = ["10.99.0.0/24", "10.99.1.0/24", "10.99.2.0/24"]
  private_subnets  = ["10.99.3.0/24", "10.99.4.0/24", "10.99.5.0/24"]
  database_subnets = ["10.99.7.0/24", "10.99.8.0/24", "10.99.9.0/24"]

  create_database_subnet_group = true
  enable_nat_gateway           = true
  single_nat_gateway           = true

  # etc, etc; see vpc module docs for more
}

# Create a database server
resource "aws_db_instance" "default" {
  # attach database_subnet (private)
  db_subnet_group_name = module.vpc.database_subnet_group_name

  # etc, etc; see aws_db_instance docs for more
}

# Create a server for bastion. (installed SSM Agent)
resource "aws_instance" "bastion" {
  # attach i am role (based on AmazonSSMMManagedInstanceCore Policy)
  iam_instance_profile = resource.aws_iam_role.ssm_role.name
  # attach private_subnet
  subnet_id = module.vpc.private_subnets[0]
  # etc, etc; see aws_instance docs for more
}

# Configure the MySQL provider based on the outcome of
# creating the aws_db_instance.
provider "mysql" {
  # endpoint's host must be localhost, and port is unused.
  endpoint = "localhost:${unused_port}"
  username = aws_db_instance.default.username
  password = aws_db_instance.default.password

  aws_ssm_session_manager_client_config {
    # No SSH keys or sshd are needed in this mode.
    mode            = "port_forwarding"
    ec2_instance_id = resource.aws_instance.bastion.id
    rds_endpoint    = resource.aws_db_instance.default.endpoint
    aws_profile     = local.aws_profile
    region          = local.region
  }
}
```

~> **Caution:** [Session Manager Plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html) is required to be installed.

See [Setting up Session Manager](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-getting-started.html) for the AWS side of the setup.

### Required AWS permissions and network

- The instance needs the SSM Agent and the `AmazonSSMManagedInstanceCore` managed policy (or equivalent).
- The caller needs `ssm:StartSession` on the instance ARN and on `arn:aws:ssm:*:*:document/AWS-StartPortForwardingSessionToRemoteHost`, plus `ssm:TerminateSession`.
- The instance's security group must allow egress to the RDS port, and the RDS security group must allow ingress from the instance.

~> **Note:** The `session-manager-plugin` process and the SSM session are closed when the provider exits. An `aws_profile` that does not exist in the shared AWS config is an error. The plugin listens on `localhost` only, so the port is not reachable from other hosts.

### `ssh` mode (legacy)

`mode` defaults to `ssh`, which tunnels SSH over an SSM session
(`AWS-StartSSHSession`). It requires sshd and an SSH key on the instance. A
future major version may change the default mode to `port_forwarding`, so set
`mode` explicitly.

```terraform
provider "mysql" {
  endpoint = "localhost:${unused_port}"
  username = aws_db_instance.default.username
  password = aws_db_instance.default.password

  aws_ssm_session_manager_client_config {
    mode            = "ssh" # the default
    ec2_instance_id = resource.aws_instance.bastion.id
    rds_endpoint    = resource.aws_db_instance.default.endpoint
    ssh_user        = local.ssh_user
    ssh_key_path    = local.ssh_key_path
  }
}
```

In `ssh` mode `remote_host` may be given as `host` (port 22 is assumed) or `host:port`. Host
keys are verified against `~/.ssh/known_hosts`; the file (and `~/.ssh`) is
created when missing and unknown hosts are added on first use. The local port
forward listener binds to `127.0.0.1` only.

## Port forward through public bastion

~> **Caution:** This is a feature in development.

Alternatively, the provider can forward the port over SSH through a public
bastion server. Set `endpoint` to `localhost:<unused port>` and configure
`port_forward_client_config`.

```terraform
# Create VPC and Subnets
module "vpc" {

  # (sample value)
  public_subnets   = ["10.99.0.0/24", "10.99.1.0/24", "10.99.2.0/24"]
  private_subnets  = ["10.99.3.0/24", "10.99.4.0/24", "10.99.5.0/24"]
  database_subnets = ["10.99.7.0/24", "10.99.8.0/24", "10.99.9.0/24"]

  create_database_subnet_group = true
  enable_nat_gateway           = false
  single_nat_gateway           = true

  # etc, etc; see vpc module docs for more
}

# Create a database server
resource "aws_db_instance" "default" {
  # attach database_subnet (private)
  db_subnet_group_name = module.vpc.database_subnet_group_name

  # etc, etc; see aws_db_instance docs for more
}

# Create a public server for bastion.
resource "aws_instance" "bastion" {
  # attach i am role (based on AmazonSSMMManagedInstanceCore Policy)
  iam_instance_profile = resource.aws_iam_role.ssm_role.name
  # attach public_subnet
  subnet_id = module.vpc.public_subnets[0]
  # etc, etc; see aws_instance docs for more
}

# Configure the MySQL provider based on the outcome of
# creating the aws_db_instance.
provider "mysql" {
  # endpoint's host must be localhost, and port is unused.
  endpoint = "localhost:${unused_port}"
  username = aws_db_instance.default.username
  password = aws_db_instance.default.password

  port_forward_client_config {
    remote_host  = resource.aws_instance.bastion.public_ip
    db_endpoint  = resource.aws_db_instance.default.endpoint
    ssh_user     = local.ssh_user
    ssh_key_path = local.ssh_key_path
  }
}
```

~> **Note:** The local port forward listener binds to `127.0.0.1` only, so it is not reachable from other hosts.

<!-- schema generated by tfplugindocs -->
## Schema

### Required

- `endpoint` (String) The address of the MySQL server to use. Most often a "hostname:port" pair, but may also be an absolute path to a Unix socket when the host OS is Unix-compatible. Can also be sourced from the `MYSQL_ENDPOINT` environment variable.
- `username` (String) Username to use to authenticate with the server, can also be sourced from the `MYSQL_USERNAME` environment variable.

### Optional

- `authentication_plugin` (String) Sets the authentication plugin, it can be one of the following: `native` or `cleartext`. Defaults to `native`.
- `aws_ssm_session_manager_client_config` (Block List, Max: 1) Configuration for use aws session manager. (see [below for nested schema](#nestedblock--aws_ssm_session_manager_client_config))
- `max_conn_lifetime_sec` (Number) Sets the maximum amount of time a connection may be reused. If d <= 0, connections are reused forever.
- `max_open_conns` (Number) Sets the maximum number of open connections to the database. If n <= 0, then there is no limit on the number of open connections.
- `password` (String) Password for the given user, if that user has a password, can also be sourced from the `MYSQL_PASSWORD` environment variable.
- `port_forward_client_config` (Block List, Max: 1) Configuration for Port Forward. (see [below for nested schema](#nestedblock--port_forward_client_config))
- `proxy` (String) Proxy socks url, can also be sourced from `ALL_PROXY` or `all_proxy` environment variables.
- `tls` (String) The TLS configuration. One of `false`, `true`, or `skip-verify`. Defaults to `false`. Can also be sourced from the `MYSQL_TLS_CONFIG` environment variable.

<a id="nestedblock--aws_ssm_session_manager_client_config"></a>
### Nested Schema for `aws_ssm_session_manager_client_config`

Required:

- `ec2_instance_id` (String) The EC2 server can connect the RDS to use. If you are managing by Terraform, you can set the value from `aws_instance`'s id.
- `rds_endpoint` (String) The endpoint of the RDS to use. If you are managing by Terraform, you can set the value from `aws_db_instance` or `aws_rds_cluster`'s endpoint.

Optional:

- `aws_profile` (String) AWS user's profile (SSO logged in), can also be sourced from the `AWS_PROFILE` or `AWS_DEFAULT_PROFILE` environment variables. If you use AWS credentials, they can also be sourced from the `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY_ID`, and `AWS_SESSION_TOKEN` environment variables.
- `mode` (String) How to reach the RDS endpoint. `ssh` (default) tunnels SSH over an SSM session (`AWS-StartSSHSession`) and needs sshd and an SSH key on the EC2 instance. `port_forwarding` uses the SSM document `AWS-StartPortForwardingSessionToRemoteHost`: it needs no SSH keys, no sshd and no `ssh_user`/`ssh_key_path`. Defaults to `ssh`; a future major version may change the default to `port_forwarding`. `port_forwarding` is experimental: it is covered by offline tests only and has not yet been verified against real AWS; please report issues.
- `region` (String) AWS region, can also be sourced from the `AWS_REGION` or `AWS_DEFAULT_REGION` environment variables.
- `ssh_key_path` (String) SSH user's private key path (`ssh` mode only). Defaults to `~/.ssh/id_rsa`.
- `ssh_user` (String) SSH user name (`ssh` mode only). Defaults to current user name.


<a id="nestedblock--port_forward_client_config"></a>
### Nested Schema for `port_forward_client_config`

Required:

- `db_endpoint` (String) The endpoint of the DB server to use.
- `remote_host` (String) The IP or host of public bastion server can connect the DB server to use. May include a port as `host:port`; defaults to port 22.

Optional:

- `mode` (String) How to reach the RDS endpoint. `ssh` (default) tunnels SSH over an SSM session (`AWS-StartSSHSession`) and needs sshd and an SSH key on the EC2 instance. `port_forwarding` uses the SSM document `AWS-StartPortForwardingSessionToRemoteHost`: it needs no SSH keys, no sshd and no `ssh_user`/`ssh_key_path`. Defaults to `ssh`; a future major version may change the default to `port_forwarding`. `port_forwarding` is experimental: it is covered by offline tests only and has not yet been verified against real AWS; please report issues.
- `ssh_key_path` (String) SSH user's private key path (`ssh` mode only). Defaults to `~/.ssh/id_rsa`.
- `ssh_user` (String) SSH user name (`ssh` mode only). Defaults to current user name.
