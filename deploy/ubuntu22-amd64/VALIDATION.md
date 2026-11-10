# AMD64 build validation

This package is built for Linux AMD64 (ELF x86-64), including Intel x64 hosts. All five executables use CGO_ENABLED=0 and require no ARM emulation.

The source regression suite, Bash syntax, executable architecture and package checksums are checked on the development host during this update. See BUILD-INFO.txt for the actual build toolchain and source state.

Ubuntu 22.04 AMD64 runtime performance, systemd installation, site certificates, routing and target-host KPI measurements remain to be verified by the deployment team using GUIDE.md. Build-host model evaluation is not a target-hardware performance measurement. Previous ARM64 deployment validation does not certify execution of this package.
