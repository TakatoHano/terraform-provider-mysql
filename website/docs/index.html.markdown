---
layout: "mysql"
page_title: "Provider: MySQL"
sidebar_current: "docs-mysql-index"
description: |-
  A provider for MySQL Server.
---

# MySQL Provider

[MySQL](http://www.mysql.com) is a relational database server. The MySQL
provider exposes resources used to manage the configuration of resources
in a MySQL server.

Use the navigation to the left to read about the available resources.

## Example Usage

The following is a minimal example:

```hcl
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
MySQL servers. For example, ``aws_db_instance`` is able to create MySQL
servers in Amazon's RDS service.

```hcl
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
  endpoint = "${aws_db_instance.default.endpoint}"
  username = "${aws_db_instance.default.username}"
  password = "${aws_db_instance.default.password}"
}

# Create a second database, in addition to the "initial_db" created
# by the aws_db_instance resource above.
resource "mysql_database" "app" {
  name = "another_db"
}
```

## SOCKS5 Proxy Support

The MySQL provider respects the `ALL_PROXY` and/or `all_proxy` environment variables.

```
$ export all_proxy="socks5://your.proxy:3306"
```

## Port Forward with AWS SSM Session Manager Support

~> **Caution:** This is a feature in development.

```hcl

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
  db_subnet_group_name       = module.vpc.database_subnet_group_name

  # etc, etc; see aws_db_instance docs for more
}

# Create a server for bastion. (installed SSM Agent)
resource "aws_instance" "bastion" {
  # attach i am role (based on AmazonSSMMManagedInstanceCore Policy)
  iam_instance_profile = resource.aws_iam_role.ssm_role.name
  # attach private_subnet
  subnet_id            = module.vpc.private_subnets[0]
  # etc, etc; see aws_instance docs for more
}

# Configure the MySQL provider based on the outcome of
# creating the aws_db_instance.
provider "mysql" {
  endpoint = "localhost:${unused_port}"
  username = "${aws_db_instance.default.username}"
  password = "${aws_db_instance.default.password}"

  aws_ssm_session_manager_client_config {
    ec2_instance_id = resource.aws_instance.bastion.id
    rds_endpoint    = resource.aws_db_instance.default.endpoint
    ssh_user        = local.ssh_user
    ssh_key_path    = local.ssh_key_path
    aws_profile     = local.aws_profile
    region          = local.region
  }
}
```
~> **Caution:** [Session Manager Plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html) is required to be installed.

## Port forward through public bastion

~> **Caution:** This is a feature in development.

```hcl

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
  db_subnet_group_name       = module.vpc.database_subnet_group_name

  # etc, etc; see aws_db_instance docs for more
}

# Create a public server for bastion.
resource "aws_instance" "bastion" {
  # attach i am role (based on AmazonSSMMManagedInstanceCore Policy)
  iam_instance_profile = resource.aws_iam_role.ssm_role.name
  # attach public_subnet
  subnet_id            = module.vpc.public_subnets[0]
  # etc, etc; see aws_instance docs for more
}

# Configure the MySQL provider based on the outcome of
# creating the aws_db_instance.
provider "mysql" {
  endpoint = "localhost:${unused_port}"
  username = "${aws_db_instance.default.username}"
  password = "${aws_db_instance.default.password}"

  port_forward_client_config {
    ec2_instance_id = resource.aws_instance.bastion.id
    db_endpoint     = resource.aws_db_instance.default.endpoint
    ssh_user        = local.ssh_user
    ssh_key_path    = local.ssh_key_path
  }
}
```


## Argument Reference

The following arguments are supported:

* `endpoint` - (Required) The address of the MySQL server to use. Most often a "hostname:port" pair, but may also be an absolute path to a Unix socket when the host OS is Unix-compatible. Can also be sourced from the `MYSQL_ENDPOINT` environment variable.
* `username` - (Required) Username to use to authenticate with the server, can also be sourced from the `MYSQL_USERNAME` environment variable.
* `password` - (Optional) Password for the given user, if that user has a password, can also be sourced from the `MYSQL_PASSWORD` environment variable.
* `proxy` - (Optional) Proxy socks url, can also be sourced from `ALL_PROXY` or `all_proxy` environment variables.
* `tls` - (Optional) The TLS configuration. One of `false`, `true`, or `skip-verify`. Defaults to `false`. Can also be sourced from the `MYSQL_TLS_CONFIG` environment variable.
* `max_conn_lifetime_sec` - (Optional) Sets the maximum amount of time a connection may be reused. If d <= 0, connections are reused forever.
* `max_open_conns` - (Optional) Sets the maximum number of open connections to the database. If n <= 0, then there is no limit on the number of open connections.
* `authentication_plugin` - (Optional) Sets the authentication plugin, it can be one of the following: `native` or `cleartext`. Defaults to `native`.
* `aws_ssm_session_manager_client_config` - (Optional) Configuration for use aws ssm session manager.
* `port_forward_client_config` - (Optional) Configuration for port forwarding through public bastion.

### aws_ssm_session_manager_client_config Argument Reference

Example:

```hcl
provider "mysql" {
  # ... other configuration ...

  # endopoint's host must be localhost, and port is unused. 
  endpoint = "localhost:${unused_port}"

  aws_ssm_session_manager_client_config {
    ec2_instance_id = resource.aws_instance.bastion.id
    rds_endpoint    = resource.aws_db_instance.default.endpoint
    ssh_user        = local.ssh_user
    ssh_key_path    = local.ssh_key_path
    aws_profile     = local.aws_profile
    region          = local.region
  }
}
```

~> **Notes.** [Setting up Session Manager.](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-getting-started.html)

* `ec2_instance_id` - (Required) The EC2 server can connect the RDS to use. If you are managing by Terraform, you can set the value from [`resource.aws_instance`](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/instance)'s endpoint.
* `rds_endpoint` - (Required) The endpoint of the RDS to use. If you are managing by Terraform, you can set the value from [`resource.aws_db_instance`](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/db_instance) or [`resource.aws_rds_cluster`](https://registry.terraform.io/providers/hashicorp/aws/latest/docs/resources/rds_cluster)'s endpoint.
* `ssh_user` - (Optional) SSH user name. Defaults to current user name.
* `ssh_key_path` - (Optional) SSH user's private key path. Default to `~/.ssh/id_rsa`
* `aws_profile` - (Optional) AWS user's profile(SSO logged in), can also be sourced from the `AWS_PROFILE` or `AWS_DEFAULT_PROFILE` environment variables. If you use AWS credential, can aloso be sourced from the `AWS_ACCESS_KEY_ID`,`AWS_SECRET_ACCESS_KEY_ID`, and `AWS_SESSION_TOKEN` environment variables.
* `region` -  (Optional) AWS region, can also be sourced from the `AWS_REGION` or `AWS_DEFAULT_REGION` environment variables.

~> **Notes.** The SSH tunnel, the `session-manager-plugin` process and the SSM session are closed when the provider exits. An `aws_profile` that does not exist in the shared AWS config is an error.

~> **Notes.** The local port forward listener binds to `127.0.0.1` only, so it is not reachable from other hosts.

### port_forward_client_config Argument Reference

Example:

```hcl
provider "mysql" {
  # ... other configuration ...

  # endpoint's host must be localhost, and port is unused. 
  endpoint = "localhost:${unused_port}"

  port_forward_client_config {
    remote_host     = resource.aws_instance.bastion.public_ip
    db_endpoint     = resource.aws_db_instance.default.endpoint
    ssh_user        = local.ssh_user
    ssh_key_path    = local.ssh_key_path
  }
}
```

* `remote_host` - (Required) The IP or host of public bastion server can connect the DB server to use.
* `db_endpoint` - (Required) The endpoint of the DB server to use.
* `ssh_user` - (Optional) SSH user name. Defaults to current user name.
* `ssh_key_path` - (Optional) SSH user's private key path. Default to `~/.ssh/id_rsa`

~> **Notes.** The local port forward listener binds to `127.0.0.1` only, so it is not reachable from other hosts.
