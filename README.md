# terraform-mysql-provider (unofficial)
==================

Forked from https://github.com/hashicorp/terraform-provider-mysql

Usage
-----
This Provider Support Port Forward with [AWS SSM Session Manager](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager.html)


```hcl
terraform {
  required_providers {
    mysql = {
      source = "TakatoHano/mysql"
    }
  }
}

provider "mysql" {
  # Configuration options
  endpoint = "localhost:${unused_port}"
  username = "${aws_db_instance.db.username}"
  password = "${aws_db_instance.db.password}"
  
  aws_ssm_session_manager_client_config {
    ec2_instance_id = resource.aws_instance.bastion.id
    rds_endpoint    = resource.aws_db_instance.db.endpoint
  }
}
```

Or, Port Forwarding through public server

```hcl
terraform {
  required_providers {
    mysql = {
      source = "TakatoHano/mysql"
    }
  }
}

provider "mysql" {
  # Configuration options
  endpoint = "localhost:${unused_port}"
  username = "${aws_db_instance.db.username}"
  password = "${aws_db_instance.db.password}"
  
  port_forward_client_config {
    remote_host = resource.aws_instance.bastion.public_ip
    db_endpoint = resource.aws_db_instance.db.endpoint
  }
}
```

## Debugging

Run the provider binary with `-debug` to start it in debugger-friendly mode; it prints the `TF_REATTACH_PROVIDERS` value to use with Terraform.
