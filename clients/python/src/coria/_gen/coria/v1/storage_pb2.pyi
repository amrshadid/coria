import datetime

from google.protobuf import duration_pb2 as _duration_pb2
from google.protobuf import field_mask_pb2 as _field_mask_pb2
from google.protobuf import timestamp_pb2 as _timestamp_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class StorageTestStep(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    STORAGE_TEST_STEP_UNSPECIFIED: _ClassVar[StorageTestStep]
    STORAGE_TEST_STEP_WRITE: _ClassVar[StorageTestStep]
    STORAGE_TEST_STEP_READ: _ClassVar[StorageTestStep]
    STORAGE_TEST_STEP_COMPARE: _ClassVar[StorageTestStep]
    STORAGE_TEST_STEP_REMOVE: _ClassVar[StorageTestStep]

class StorageTestOutcome(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    STORAGE_TEST_OUTCOME_UNSPECIFIED: _ClassVar[StorageTestOutcome]
    STORAGE_TEST_OUTCOME_PASSED: _ClassVar[StorageTestOutcome]
    STORAGE_TEST_OUTCOME_FAILED: _ClassVar[StorageTestOutcome]
    STORAGE_TEST_OUTCOME_SKIPPED: _ClassVar[StorageTestOutcome]

class StorageFault(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    STORAGE_FAULT_UNSPECIFIED: _ClassVar[StorageFault]
    STORAGE_FAULT_UNREACHABLE: _ClassVar[StorageFault]
    STORAGE_FAULT_TLS_FAILED: _ClassVar[StorageFault]
    STORAGE_FAULT_REFUSED: _ClassVar[StorageFault]
    STORAGE_FAULT_NOT_FOUND: _ClassVar[StorageFault]
    STORAGE_FAULT_WRONG_ANSWER: _ClassVar[StorageFault]
    STORAGE_FAULT_ADDRESS_NOT_ALLOWED: _ClassVar[StorageFault]
STORAGE_TEST_STEP_UNSPECIFIED: StorageTestStep
STORAGE_TEST_STEP_WRITE: StorageTestStep
STORAGE_TEST_STEP_READ: StorageTestStep
STORAGE_TEST_STEP_COMPARE: StorageTestStep
STORAGE_TEST_STEP_REMOVE: StorageTestStep
STORAGE_TEST_OUTCOME_UNSPECIFIED: StorageTestOutcome
STORAGE_TEST_OUTCOME_PASSED: StorageTestOutcome
STORAGE_TEST_OUTCOME_FAILED: StorageTestOutcome
STORAGE_TEST_OUTCOME_SKIPPED: StorageTestOutcome
STORAGE_FAULT_UNSPECIFIED: StorageFault
STORAGE_FAULT_UNREACHABLE: StorageFault
STORAGE_FAULT_TLS_FAILED: StorageFault
STORAGE_FAULT_REFUSED: StorageFault
STORAGE_FAULT_NOT_FOUND: StorageFault
STORAGE_FAULT_WRONG_ANSWER: StorageFault
STORAGE_FAULT_ADDRESS_NOT_ALLOWED: StorageFault

class Storage(_message.Message):
    __slots__ = ("name", "directory", "s3", "capacity_bytes", "create_time", "update_time")
    NAME_FIELD_NUMBER: _ClassVar[int]
    DIRECTORY_FIELD_NUMBER: _ClassVar[int]
    S3_FIELD_NUMBER: _ClassVar[int]
    CAPACITY_BYTES_FIELD_NUMBER: _ClassVar[int]
    CREATE_TIME_FIELD_NUMBER: _ClassVar[int]
    UPDATE_TIME_FIELD_NUMBER: _ClassVar[int]
    name: str
    directory: DirectoryStorage
    s3: S3Storage
    capacity_bytes: int
    create_time: _timestamp_pb2.Timestamp
    update_time: _timestamp_pb2.Timestamp
    def __init__(self, name: _Optional[str] = ..., directory: _Optional[_Union[DirectoryStorage, _Mapping]] = ..., s3: _Optional[_Union[S3Storage, _Mapping]] = ..., capacity_bytes: _Optional[int] = ..., create_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., update_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class DirectoryStorage(_message.Message):
    __slots__ = ("root", "path")
    ROOT_FIELD_NUMBER: _ClassVar[int]
    PATH_FIELD_NUMBER: _ClassVar[int]
    root: str
    path: str
    def __init__(self, root: _Optional[str] = ..., path: _Optional[str] = ...) -> None: ...

class S3Storage(_message.Message):
    __slots__ = ("endpoint", "region", "bucket", "prefix", "path_style", "access_key_id", "secret_access_key", "secret_access_key_set")
    ENDPOINT_FIELD_NUMBER: _ClassVar[int]
    REGION_FIELD_NUMBER: _ClassVar[int]
    BUCKET_FIELD_NUMBER: _ClassVar[int]
    PREFIX_FIELD_NUMBER: _ClassVar[int]
    PATH_STYLE_FIELD_NUMBER: _ClassVar[int]
    ACCESS_KEY_ID_FIELD_NUMBER: _ClassVar[int]
    SECRET_ACCESS_KEY_FIELD_NUMBER: _ClassVar[int]
    SECRET_ACCESS_KEY_SET_FIELD_NUMBER: _ClassVar[int]
    endpoint: str
    region: str
    bucket: str
    prefix: str
    path_style: bool
    access_key_id: str
    secret_access_key: str
    secret_access_key_set: bool
    def __init__(self, endpoint: _Optional[str] = ..., region: _Optional[str] = ..., bucket: _Optional[str] = ..., prefix: _Optional[str] = ..., path_style: _Optional[bool] = ..., access_key_id: _Optional[str] = ..., secret_access_key: _Optional[str] = ..., secret_access_key_set: _Optional[bool] = ...) -> None: ...

class StorageTestResult(_message.Message):
    __slots__ = ("step", "outcome", "duration", "fault")
    STEP_FIELD_NUMBER: _ClassVar[int]
    OUTCOME_FIELD_NUMBER: _ClassVar[int]
    DURATION_FIELD_NUMBER: _ClassVar[int]
    FAULT_FIELD_NUMBER: _ClassVar[int]
    step: StorageTestStep
    outcome: StorageTestOutcome
    duration: _duration_pb2.Duration
    fault: StorageFault
    def __init__(self, step: _Optional[_Union[StorageTestStep, str]] = ..., outcome: _Optional[_Union[StorageTestOutcome, str]] = ..., duration: _Optional[_Union[datetime.timedelta, _duration_pb2.Duration, _Mapping]] = ..., fault: _Optional[_Union[StorageFault, str]] = ...) -> None: ...

class AddStorageRequest(_message.Message):
    __slots__ = ("tenant", "storage")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    STORAGE_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    storage: Storage
    def __init__(self, tenant: _Optional[str] = ..., storage: _Optional[_Union[Storage, _Mapping]] = ...) -> None: ...

class AddStorageResponse(_message.Message):
    __slots__ = ("storage",)
    STORAGE_FIELD_NUMBER: _ClassVar[int]
    storage: Storage
    def __init__(self, storage: _Optional[_Union[Storage, _Mapping]] = ...) -> None: ...

class GetStorageRequest(_message.Message):
    __slots__ = ("tenant", "name")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    name: str
    def __init__(self, tenant: _Optional[str] = ..., name: _Optional[str] = ...) -> None: ...

class GetStorageResponse(_message.Message):
    __slots__ = ("storage",)
    STORAGE_FIELD_NUMBER: _ClassVar[int]
    storage: Storage
    def __init__(self, storage: _Optional[_Union[Storage, _Mapping]] = ...) -> None: ...

class ListStoragesRequest(_message.Message):
    __slots__ = ("tenant", "page_size", "page_token")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    page_size: int
    page_token: str
    def __init__(self, tenant: _Optional[str] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListStoragesResponse(_message.Message):
    __slots__ = ("storages", "next_page_token")
    STORAGES_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    storages: _containers.RepeatedCompositeFieldContainer[Storage]
    next_page_token: str
    def __init__(self, storages: _Optional[_Iterable[_Union[Storage, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class SetStorageRequest(_message.Message):
    __slots__ = ("tenant", "storage", "update_mask")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    STORAGE_FIELD_NUMBER: _ClassVar[int]
    UPDATE_MASK_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    storage: Storage
    update_mask: _field_mask_pb2.FieldMask
    def __init__(self, tenant: _Optional[str] = ..., storage: _Optional[_Union[Storage, _Mapping]] = ..., update_mask: _Optional[_Union[_field_mask_pb2.FieldMask, _Mapping]] = ...) -> None: ...

class SetStorageResponse(_message.Message):
    __slots__ = ("storage",)
    STORAGE_FIELD_NUMBER: _ClassVar[int]
    storage: Storage
    def __init__(self, storage: _Optional[_Union[Storage, _Mapping]] = ...) -> None: ...

class RemoveStorageRequest(_message.Message):
    __slots__ = ("tenant", "name")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    name: str
    def __init__(self, tenant: _Optional[str] = ..., name: _Optional[str] = ...) -> None: ...

class RemoveStorageResponse(_message.Message):
    __slots__ = ("storage",)
    STORAGE_FIELD_NUMBER: _ClassVar[int]
    storage: Storage
    def __init__(self, storage: _Optional[_Union[Storage, _Mapping]] = ...) -> None: ...

class TestStorageRequest(_message.Message):
    __slots__ = ("tenant", "name")
    TENANT_FIELD_NUMBER: _ClassVar[int]
    NAME_FIELD_NUMBER: _ClassVar[int]
    tenant: str
    name: str
    def __init__(self, tenant: _Optional[str] = ..., name: _Optional[str] = ...) -> None: ...

class TestStorageResponse(_message.Message):
    __slots__ = ("passed", "results")
    PASSED_FIELD_NUMBER: _ClassVar[int]
    RESULTS_FIELD_NUMBER: _ClassVar[int]
    passed: bool
    results: _containers.RepeatedCompositeFieldContainer[StorageTestResult]
    def __init__(self, passed: _Optional[bool] = ..., results: _Optional[_Iterable[_Union[StorageTestResult, _Mapping]]] = ...) -> None: ...
