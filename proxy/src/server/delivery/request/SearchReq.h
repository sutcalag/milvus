// Copyright (C) 2019-2020 Zilliz. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance
// with the License. You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software distributed under the License
// is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express
// or implied. See the License for the specific language governing permissions and limitations under the License.

#pragma once

#include "server/delivery/request/BaseReq.h"

#include <map>
#include <memory>
#include <string>
#include <vector>
// #include "db/snapshot/Context.h"

namespace milvus {
namespace server {

class SearchReq : public BaseReq {
 public:
    static BaseReqPtr
    Create(const ContextPtr& context, const ::milvus::grpc::SearchParam *request,
           ::milvus::grpc::QueryResult *response);

 protected:
    SearchReq(const ContextPtr& context, const ::milvus::grpc::SearchParam *request,
              ::milvus::grpc::QueryResult *result);

    Status
    OnExecute() override;

 private:
    const ::milvus::grpc::SearchParam *request_;
    ::milvus::grpc::QueryResult* result_;
};

}  // namespace server
}  // namespace milvus