/**
 * Copyright 2021 Opstrace, Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

import { ExporterCloudWatchForm } from "./Form";
import { ExporterCloudWatchShow } from "./Show";
import ExporterCloudWatchStatus from "./Status";
import ExporterCloudWatchLogo from "./Logo.png";

import { IntegrationDef } from "../types";

export const exporterCloudWatchIntegration: IntegrationDef = {
  kind: "exporter-cloud-watch",
  category: "exporter",
  label: "Amazon CloudWatch",
  desc: "An exporter for Amazon CloudWatch, for Prometheus.",
  Form: ExporterCloudWatchForm,
  Show: ExporterCloudWatchShow,
  Status: ExporterCloudWatchStatus,
  enabled: true,
  Logo: ExporterCloudWatchLogo
};