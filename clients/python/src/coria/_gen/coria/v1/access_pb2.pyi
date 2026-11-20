import datetime

from google.protobuf import duration_pb2 as _duration_pb2
from google.protobuf import timestamp_pb2 as _timestamp_pb2
from coria._gen.coria.v1 import scope_pb2 as _scope_pb2
from google.protobuf.internal import containers as _containers
from google.protobuf.internal import enum_type_wrapper as _enum_type_wrapper
from google.protobuf import descriptor as _descriptor
from google.protobuf import message as _message
from collections.abc import Iterable as _Iterable, Mapping as _Mapping
from typing import ClassVar as _ClassVar, Optional as _Optional, Union as _Union

DESCRIPTOR: _descriptor.FileDescriptor

class Role(int, metaclass=_enum_type_wrapper.EnumTypeWrapper):
    __slots__ = ()
    ROLE_UNSPECIFIED: _ClassVar[Role]
    ROLE_READER: _ClassVar[Role]
    ROLE_WRITER: _ClassVar[Role]
    ROLE_OPERATOR: _ClassVar[Role]
    ROLE_ADMIN: _ClassVar[Role]
ROLE_UNSPECIFIED: Role
ROLE_READER: Role
ROLE_WRITER: Role
ROLE_OPERATOR: Role
ROLE_ADMIN: Role

class Key(_message.Message):
    __slots__ = ("id", "holder", "create_time", "expire_time", "last_use_time", "revoke_time")
    ID_FIELD_NUMBER: _ClassVar[int]
    HOLDER_FIELD_NUMBER: _ClassVar[int]
    CREATE_TIME_FIELD_NUMBER: _ClassVar[int]
    EXPIRE_TIME_FIELD_NUMBER: _ClassVar[int]
    LAST_USE_TIME_FIELD_NUMBER: _ClassVar[int]
    REVOKE_TIME_FIELD_NUMBER: _ClassVar[int]
    id: str
    holder: str
    create_time: _timestamp_pb2.Timestamp
    expire_time: _timestamp_pb2.Timestamp
    last_use_time: _timestamp_pb2.Timestamp
    revoke_time: _timestamp_pb2.Timestamp
    def __init__(self, id: _Optional[str] = ..., holder: _Optional[str] = ..., create_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., expire_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., last_use_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ..., revoke_time: _Optional[_Union[datetime.datetime, _timestamp_pb2.Timestamp, _Mapping]] = ...) -> None: ...

class Grant(_message.Message):
    __slots__ = ("key_id", "scope", "role")
    KEY_ID_FIELD_NUMBER: _ClassVar[int]
    SCOPE_FIELD_NUMBER: _ClassVar[int]
    ROLE_FIELD_NUMBER: _ClassVar[int]
    key_id: str
    scope: _scope_pb2.Scope
    role: Role
    def __init__(self, key_id: _Optional[str] = ..., scope: _Optional[_Union[_scope_pb2.Scope, _Mapping]] = ..., role: _Optional[_Union[Role, str]] = ...) -> None: ...

class Principal(_message.Message):
    __slots__ = ("key", "local_operator", "grants")
    KEY_FIELD_NUMBER: _ClassVar[int]
    LOCAL_OPERATOR_FIELD_NUMBER: _ClassVar[int]
    GRANTS_FIELD_NUMBER: _ClassVar[int]
    key: Key
    local_operator: LocalOperator
    grants: _containers.RepeatedCompositeFieldContainer[Grant]
    def __init__(self, key: _Optional[_Union[Key, _Mapping]] = ..., local_operator: _Optional[_Union[LocalOperator, _Mapping]] = ..., grants: _Optional[_Iterable[_Union[Grant, _Mapping]]] = ...) -> None: ...

class LocalOperator(_message.Message):
    __slots__ = ("user_id",)
    USER_ID_FIELD_NUMBER: _ClassVar[int]
    user_id: str
    def __init__(self, user_id: _Optional[str] = ...) -> None: ...

class CreateKeyRequest(_message.Message):
    __slots__ = ("holder", "expire_after", "never_expire")
    HOLDER_FIELD_NUMBER: _ClassVar[int]
    EXPIRE_AFTER_FIELD_NUMBER: _ClassVar[int]
    NEVER_EXPIRE_FIELD_NUMBER: _ClassVar[int]
    holder: str
    expire_after: _duration_pb2.Duration
    never_expire: bool
    def __init__(self, holder: _Optional[str] = ..., expire_after: _Optional[_Union[datetime.timedelta, _duration_pb2.Duration, _Mapping]] = ..., never_expire: _Optional[bool] = ...) -> None: ...

class CreateKeyResponse(_message.Message):
    __slots__ = ("key", "secret")
    KEY_FIELD_NUMBER: _ClassVar[int]
    SECRET_FIELD_NUMBER: _ClassVar[int]
    key: Key
    secret: str
    def __init__(self, key: _Optional[_Union[Key, _Mapping]] = ..., secret: _Optional[str] = ...) -> None: ...

class GetKeyRequest(_message.Message):
    __slots__ = ("id",)
    ID_FIELD_NUMBER: _ClassVar[int]
    id: str
    def __init__(self, id: _Optional[str] = ...) -> None: ...

class GetKeyResponse(_message.Message):
    __slots__ = ("key",)
    KEY_FIELD_NUMBER: _ClassVar[int]
    key: Key
    def __init__(self, key: _Optional[_Union[Key, _Mapping]] = ...) -> None: ...

class ListKeysRequest(_message.Message):
    __slots__ = ("page_size", "page_token")
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    page_size: int
    page_token: str
    def __init__(self, page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListKeysResponse(_message.Message):
    __slots__ = ("keys", "next_page_token")
    KEYS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    keys: _containers.RepeatedCompositeFieldContainer[Key]
    next_page_token: str
    def __init__(self, keys: _Optional[_Iterable[_Union[Key, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class RevokeKeyRequest(_message.Message):
    __slots__ = ("id",)
    ID_FIELD_NUMBER: _ClassVar[int]
    id: str
    def __init__(self, id: _Optional[str] = ...) -> None: ...

class RevokeKeyResponse(_message.Message):
    __slots__ = ("key",)
    KEY_FIELD_NUMBER: _ClassVar[int]
    key: Key
    def __init__(self, key: _Optional[_Union[Key, _Mapping]] = ...) -> None: ...

class SetGrantRequest(_message.Message):
    __slots__ = ("key_id", "scope", "role")
    KEY_ID_FIELD_NUMBER: _ClassVar[int]
    SCOPE_FIELD_NUMBER: _ClassVar[int]
    ROLE_FIELD_NUMBER: _ClassVar[int]
    key_id: str
    scope: _scope_pb2.Scope
    role: Role
    def __init__(self, key_id: _Optional[str] = ..., scope: _Optional[_Union[_scope_pb2.Scope, _Mapping]] = ..., role: _Optional[_Union[Role, str]] = ...) -> None: ...

class SetGrantResponse(_message.Message):
    __slots__ = ("grant", "previous_role")
    GRANT_FIELD_NUMBER: _ClassVar[int]
    PREVIOUS_ROLE_FIELD_NUMBER: _ClassVar[int]
    grant: Grant
    previous_role: Role
    def __init__(self, grant: _Optional[_Union[Grant, _Mapping]] = ..., previous_role: _Optional[_Union[Role, str]] = ...) -> None: ...

class ListGrantsRequest(_message.Message):
    __slots__ = ("key_id", "scope", "page_size", "page_token")
    KEY_ID_FIELD_NUMBER: _ClassVar[int]
    SCOPE_FIELD_NUMBER: _ClassVar[int]
    PAGE_SIZE_FIELD_NUMBER: _ClassVar[int]
    PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    key_id: str
    scope: _scope_pb2.Scope
    page_size: int
    page_token: str
    def __init__(self, key_id: _Optional[str] = ..., scope: _Optional[_Union[_scope_pb2.Scope, _Mapping]] = ..., page_size: _Optional[int] = ..., page_token: _Optional[str] = ...) -> None: ...

class ListGrantsResponse(_message.Message):
    __slots__ = ("grants", "next_page_token")
    GRANTS_FIELD_NUMBER: _ClassVar[int]
    NEXT_PAGE_TOKEN_FIELD_NUMBER: _ClassVar[int]
    grants: _containers.RepeatedCompositeFieldContainer[Grant]
    next_page_token: str
    def __init__(self, grants: _Optional[_Iterable[_Union[Grant, _Mapping]]] = ..., next_page_token: _Optional[str] = ...) -> None: ...

class RemoveGrantRequest(_message.Message):
    __slots__ = ("key_id", "scope")
    KEY_ID_FIELD_NUMBER: _ClassVar[int]
    SCOPE_FIELD_NUMBER: _ClassVar[int]
    key_id: str
    scope: _scope_pb2.Scope
    def __init__(self, key_id: _Optional[str] = ..., scope: _Optional[_Union[_scope_pb2.Scope, _Mapping]] = ...) -> None: ...

class RemoveGrantResponse(_message.Message):
    __slots__ = ("previous_role",)
    PREVIOUS_ROLE_FIELD_NUMBER: _ClassVar[int]
    previous_role: Role
    def __init__(self, previous_role: _Optional[_Union[Role, str]] = ...) -> None: ...

class WhoAmIRequest(_message.Message):
    __slots__ = ()
    def __init__(self) -> None: ...

class WhoAmIResponse(_message.Message):
    __slots__ = ("principal",)
    PRINCIPAL_FIELD_NUMBER: _ClassVar[int]
    principal: Principal
    def __init__(self, principal: _Optional[_Union[Principal, _Mapping]] = ...) -> None: ...
